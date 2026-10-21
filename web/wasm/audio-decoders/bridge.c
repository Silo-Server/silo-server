// Thin C bridge between the browser decode engine and FFmpeg's audio decoders.
//
// One SadDecoder wraps one libavcodec decoder. The worker copies a compressed
// packet into sad_packet_buffer(), sends it, then drains frames with
// sad_receive_frame(). Every decoded frame is converted here to planar float32
// so the TypeScript side handles a single sample format whatever the decoder
// emits (FLTP for AC-3/E-AC-3/DTS core, S32P for DTS-HD MA, S32/S16 for
// TrueHD/MLP). Timestamps travel as doubles in microseconds: they stay exact
// well past any media duration and avoid 64-bit integers across the JS boundary.

#include <emscripten.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "libavcodec/avcodec.h"
#include "libavutil/channel_layout.h"
#include "libavutil/log.h"
#include "libavutil/mem.h"
#include "libavutil/samplefmt.h"

enum {
  SAD_CODEC_AC3 = 1,
  SAD_CODEC_EAC3 = 2,
  SAD_CODEC_DTS = 3,
  SAD_CODEC_TRUEHD = 4,
  SAD_CODEC_MLP = 5,
};

typedef struct {
  AVCodecContext *codec;
  AVPacket *packet;
  AVFrame *frame;
  float *pcm;
  int pcm_capacity;
  int samples;
  int channels;
  int sample_rate;
  uint64_t layout_mask;
  double pts_us;
} SadDecoder;

static enum AVCodecID codec_id_for(int codec) {
  switch (codec) {
    case SAD_CODEC_AC3:
      return AV_CODEC_ID_AC3;
    case SAD_CODEC_EAC3:
      return AV_CODEC_ID_EAC3;
    case SAD_CODEC_DTS:
      return AV_CODEC_ID_DTS;
    case SAD_CODEC_TRUEHD:
      return AV_CODEC_ID_TRUEHD;
    case SAD_CODEC_MLP:
      return AV_CODEC_ID_MLP;
    default:
      return AV_CODEC_ID_NONE;
  }
}

static void sad_free(SadDecoder *d) {
  if (!d) return;
  av_frame_free(&d->frame);
  av_packet_free(&d->packet);
  avcodec_free_context(&d->codec);
  free(d->pcm);
  free(d);
}

EMSCRIPTEN_KEEPALIVE
SadDecoder *sad_open(int codec, int sample_rate, int channels, const uint8_t *extradata,
                     int extradata_size) {
  // Decoders log expected conditions, such as TrueHD skipping frames until the
  // first major sync after a seek, which would otherwise reach the console.
  av_log_set_level(AV_LOG_QUIET);
  const AVCodec *decoder = avcodec_find_decoder(codec_id_for(codec));
  if (!decoder) return NULL;

  SadDecoder *d = calloc(1, sizeof(SadDecoder));
  if (!d) return NULL;
  d->codec = avcodec_alloc_context3(decoder);
  d->packet = av_packet_alloc();
  d->frame = av_frame_alloc();
  if (!d->codec || !d->packet || !d->frame) {
    sad_free(d);
    return NULL;
  }

  // Container hints only; every decoder here reads the real values from the
  // bitstream and reports them on each frame.
  if (sample_rate > 0) d->codec->sample_rate = sample_rate;
  if (channels > 0) av_channel_layout_default(&d->codec->ch_layout, channels);
  d->codec->pkt_timebase = (AVRational){1, 1000000};
  if (extradata && extradata_size > 0) {
    d->codec->extradata = av_mallocz(extradata_size + AV_INPUT_BUFFER_PADDING_SIZE);
    if (!d->codec->extradata) {
      sad_free(d);
      return NULL;
    }
    memcpy(d->codec->extradata, extradata, extradata_size);
    d->codec->extradata_size = extradata_size;
  }

  if (avcodec_open2(d->codec, decoder, NULL) < 0) {
    sad_free(d);
    return NULL;
  }
  return d;
}

EMSCRIPTEN_KEEPALIVE
uint8_t *sad_packet_buffer(SadDecoder *d, int size) {
  av_packet_unref(d->packet);
  if (av_new_packet(d->packet, size) < 0) return NULL;
  return d->packet->data;
}

EMSCRIPTEN_KEEPALIVE
int sad_send_packet(SadDecoder *d, double pts_us) {
  d->packet->pts = (int64_t)pts_us;
  int ret = avcodec_send_packet(d->codec, d->packet);
  av_packet_unref(d->packet);
  return ret;
}

EMSCRIPTEN_KEEPALIVE
int sad_send_eof(SadDecoder *d) { return avcodec_send_packet(d->codec, NULL); }

static float sample_to_float(const uint8_t *data, enum AVSampleFormat format, int index) {
  switch (format) {
    case AV_SAMPLE_FMT_U8:
    case AV_SAMPLE_FMT_U8P:
      return ((int)data[index] - 128) / 128.0f;
    case AV_SAMPLE_FMT_S16:
    case AV_SAMPLE_FMT_S16P:
      return ((const int16_t *)data)[index] / 32768.0f;
    case AV_SAMPLE_FMT_S32:
    case AV_SAMPLE_FMT_S32P:
      return (float)(((const int32_t *)data)[index] / 2147483648.0);
    case AV_SAMPLE_FMT_FLT:
    case AV_SAMPLE_FMT_FLTP:
      return ((const float *)data)[index];
    case AV_SAMPLE_FMT_DBL:
    case AV_SAMPLE_FMT_DBLP:
      return (float)((const double *)data)[index];
    default:
      return 0.0f;
  }
}

// Returns the decoded sample count (> 0) when a frame is ready, 0 when the
// decoder needs more input, and a negative AVERROR otherwise (AVERROR_EOF once
// a flush has drained everything).
EMSCRIPTEN_KEEPALIVE
int sad_receive_frame(SadDecoder *d) {
  int ret = avcodec_receive_frame(d->codec, d->frame);
  if (ret == AVERROR(EAGAIN)) return 0;
  if (ret < 0) return ret;

  AVFrame *frame = d->frame;
  const int channels = frame->ch_layout.nb_channels;
  const int samples = frame->nb_samples;
  const enum AVSampleFormat format = frame->format;
  if (channels <= 0 || samples <= 0) {
    av_frame_unref(frame);
    return 0;
  }

  const int needed = channels * samples;
  if (needed > d->pcm_capacity) {
    float *grown = realloc(d->pcm, (size_t)needed * sizeof(float));
    if (!grown) {
      av_frame_unref(frame);
      return AVERROR(ENOMEM);
    }
    d->pcm = grown;
    d->pcm_capacity = needed;
  }

  if (av_sample_fmt_is_planar(format)) {
    for (int c = 0; c < channels; c++) {
      float *out = d->pcm + (size_t)c * samples;
      const uint8_t *plane = frame->extended_data[c];
      if (format == AV_SAMPLE_FMT_FLTP) {
        memcpy(out, plane, (size_t)samples * sizeof(float));
      } else {
        for (int i = 0; i < samples; i++) out[i] = sample_to_float(plane, format, i);
      }
    }
  } else {
    const uint8_t *data = frame->extended_data[0];
    for (int i = 0; i < samples; i++) {
      for (int c = 0; c < channels; c++) {
        d->pcm[(size_t)c * samples + i] = sample_to_float(data, format, i * channels + c);
      }
    }
  }

  d->samples = samples;
  d->channels = channels;
  d->sample_rate = frame->sample_rate;
  d->layout_mask = frame->ch_layout.order == AV_CHANNEL_ORDER_NATIVE ? frame->ch_layout.u.mask : 0;
  d->pts_us = frame->pts == AV_NOPTS_VALUE ? -1.0 : (double)frame->pts;
  av_frame_unref(frame);
  return samples;
}

EMSCRIPTEN_KEEPALIVE
float *sad_frame_pcm(SadDecoder *d) { return d->pcm; }

EMSCRIPTEN_KEEPALIVE
int sad_frame_channels(SadDecoder *d) { return d->channels; }

EMSCRIPTEN_KEEPALIVE
int sad_frame_sample_rate(SadDecoder *d) { return d->sample_rate; }

// The native channel mask, split because a 64-bit value cannot cross the
// boundary as a plain number. Zero means the decoder reported no native order.
EMSCRIPTEN_KEEPALIVE
uint32_t sad_frame_layout_low(SadDecoder *d) { return (uint32_t)(d->layout_mask & 0xffffffffu); }

EMSCRIPTEN_KEEPALIVE
uint32_t sad_frame_layout_high(SadDecoder *d) { return (uint32_t)(d->layout_mask >> 32); }

// Microseconds, or -1 when the decoder could not attribute a timestamp.
EMSCRIPTEN_KEEPALIVE
double sad_frame_pts_us(SadDecoder *d) { return d->pts_us; }

EMSCRIPTEN_KEEPALIVE
void sad_flush(SadDecoder *d) { avcodec_flush_buffers(d->codec); }

EMSCRIPTEN_KEEPALIVE
void sad_close(SadDecoder *d) { sad_free(d); }

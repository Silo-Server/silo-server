package transcodenode

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"

	"github.com/Silo-Server/silo-server/internal/playback"
)

// capabilityBuildLock keeps re-probe's ordinary Lock/Unlock contract while
// allowing an abandoned inventory reader to leave the queue without a job.
type capabilityBuildLock struct {
	once sync.Once
	held chan struct{}
}

func (m *capabilityBuildLock) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.held = make(chan struct{}, 1) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.held <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	}
}
func (m *capabilityBuildLock) Lock()   { _ = m.LockContext(context.Background()) }
func (m *capabilityBuildLock) Unlock() { <-m.held }

type capabilityInputs struct {
	ffmpeg, backend, device string
	executable              string
	file                    os.FileInfo
}

type completeCapabilitySnapshot struct {
	inputs capabilityInputs
	// Store the completed probe data independently of returned slices and the
	// runtime transport features, which are projected afresh on every read.
	encoded []byte
}

func (s *Server) capabilityInputs() capabilityInputs {
	inputs := capabilityInputs{backend: playback.HWAccelNone}
	if cfg := s.runtimeConfig().Config(); cfg != nil {
		inputs.ffmpeg, inputs.backend, inputs.device = cfg.Playback.FFmpegPath, cfg.Playback.HWAccel, cfg.Playback.HWDevice
	}
	inputs.executable, _ = exec.LookPath(playback.ResolveFFmpegPath(inputs.ffmpeg))
	inputs.file, _ = os.Stat(inputs.executable)
	return inputs
}

func (a capabilityInputs) matches(b capabilityInputs) bool {
	return a.ffmpeg == b.ffmpeg && a.backend == b.backend && a.device == b.device && a.executable == b.executable &&
		a.file != nil && b.file != nil && os.SameFile(a.file, b.file) && a.file.Size() == b.file.Size() &&
		a.file.ModTime().Equal(b.file.ModTime()) && a.file.Mode() == b.file.Mode()
}

func (s *Server) projectCapabilityTransport(info playback.HWAccelInfo) playback.HWAccelInfo {
	info.TransportFeatures = nil
	if s.nodeRowID != nil {
		if nodeID, ok := s.nodeRowID(); ok && nodeID > 0 {
			info.TransportFeatures = append(info.TransportFeatures, playback.TransportFeatureProgressiveRemuxExecutionV1)
			if s.themeInputs != nil {
				info.TransportFeatures = append(info.TransportFeatures, playback.TransportFeatureThemeAudioExecutionV1)
			}
		}
	}
	info.TransportFeatures = append(info.TransportFeatures, playback.TransportFeaturePreparedTracksV1,
		playback.TransportFeatureTrickplayExtractV1, playback.TransportFeatureTrickplayExtractV2)
	info.CapabilityHash = playback.ComputeCapabilityHash(info)
	return info
}

// cachedCapabilitySnapshotLocked is only called after actual job admission was
// refused. Spare capacity always takes the ordinary assembly/probe path.
func (s *Server) cachedCapabilitySnapshotLocked(ctx context.Context, inputs capabilityInputs) (playback.HWAccelInfo, error) {
	if err := ctx.Err(); err != nil {
		return playback.HWAccelInfo{}, err
	}
	cached := s.completeCapabilitySnapshot
	if cached == nil || !cached.inputs.matches(inputs) {
		return playback.HWAccelInfo{}, ErrCapabilityBuildBusy
	}
	var info playback.HWAccelInfo
	if err := json.Unmarshal(cached.encoded, &info); err != nil {
		return playback.HWAccelInfo{}, err
	}
	info = s.projectCapabilityTransport(info)
	if err := s.checkCapabilityInputs(ctx, inputs); err != nil {
		return playback.HWAccelInfo{}, err
	}
	s.storeCapabilityHash(info.CapabilityHash)
	return info, nil
}

// Both the report and its advertised hash are published while the build lock
// remains held. A slow HTTP encoder cannot later overwrite a newer re-probe.
func (s *Server) publishCapabilitySnapshotLocked(ctx context.Context, inputs capabilityInputs, info playback.HWAccelInfo) (playback.HWAccelInfo, error) {
	if err := s.checkCapabilityInputs(ctx, inputs); err != nil {
		return playback.HWAccelInfo{}, err
	}
	encoded, _ := json.Marshal(info)
	info = s.projectCapabilityTransport(info)
	if err := s.checkCapabilityInputs(ctx, inputs); err != nil {
		return playback.HWAccelInfo{}, err
	}
	s.completeCapabilitySnapshot = &completeCapabilitySnapshot{inputs: inputs, encoded: encoded}
	s.storeCapabilityHash(info.CapabilityHash)
	return info, nil
}

func (s *Server) checkCapabilityInputs(ctx context.Context, inputs capabilityInputs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !inputs.matches(s.capabilityInputs()) {
		return errors.New("capability build inputs changed")
	}
	return nil
}

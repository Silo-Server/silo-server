# Builds `mobitool.wasm` — libmobi's mobitool compiled to wasm32-wasi — used by
# the in-process Kindle->EPUB converter (internal/ebookconvert) via wazero.
#
# The output is architecture-independent wasm32 bytecode: build it on any host
# (CI is amd64), run it on any server arch through wazero (pure Go).
#
# Build + extract the artifact:
#   docker build --platform linux/amd64 -t mobitool-wasm tools/mobitool-wasm
#   id=$(docker create mobitool-wasm); docker cp "$id:/out/mobitool.wasm" \
#     internal/ebookconvert/mobitool.wasm; docker rm "$id"
#
# Pins (bump deliberately; the converter cache key includes these):
#   wasi-sdk 25.0 · libmobi 906274205c11944b628da1c553b255acb1af7c55 · zlib 1.3.1
#
# Proven build config (see ../../docs/superpowers/specs/2026-06-17-kindle-epub-conversion-design.md):
#   --with-libxml2=no  -> internal xmlwriter provides OPF/EPUB support (-e)
#   zlib built to wasm -> avoids libmobi's bundled-miniz duplicate-symbol clash
#                         with mobitool's own miniz (EPUB zip) at wasm-ld time.

FROM debian:bookworm-slim AS build

ARG WASI_SDK_VERSION=25
ARG LIBMOBI_COMMIT=906274205c11944b628da1c553b255acb1af7c55
ARG ZLIB_VERSION=1.3.1

RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq --no-install-recommends \
        build-essential autoconf automake libtool pkg-config git curl ca-certificates xz-utils \
    && rm -rf /var/lib/apt/lists/*

# --- wasi-sdk (clang + wasm32-wasi sysroot) ---
RUN cd /opt \
    && curl -fsSL -o wasi-sdk.tar.gz \
        "https://github.com/WebAssembly/wasi-sdk/releases/download/wasi-sdk-${WASI_SDK_VERSION}/wasi-sdk-${WASI_SDK_VERSION}.0-x86_64-linux.tar.gz" \
    && tar xzf wasi-sdk.tar.gz && mv "wasi-sdk-${WASI_SDK_VERSION}.0-x86_64-linux" wasi-sdk && rm wasi-sdk.tar.gz
ENV WASI=/opt/wasi-sdk
ENV SYSROOT=/opt/wasi-sdk/share/wasi-sysroot

# --- zlib -> wasm (compile sources directly; zlib's configure runs target binaries) ---
RUN cd /opt \
    && curl -fsSL -o zlib.tar.gz "https://github.com/madler/zlib/releases/download/v${ZLIB_VERSION}/zlib-${ZLIB_VERSION}.tar.gz" \
    && tar xzf zlib.tar.gz && cd "zlib-${ZLIB_VERSION}" \
    && for s in adler32 compress crc32 deflate gzclose gzlib gzread gzwrite infback inffast inflate inftrees trees uncompr zutil; do \
         "$WASI/bin/clang" -O2 --sysroot="$SYSROOT" -DHAVE_UNISTD_H -c "$s.c" -o "$s.o"; done \
    && "$WASI/bin/llvm-ar" rcs libz.a *.o \
    && mkdir -p /opt/zlibwasm/lib /opt/zlibwasm/include \
    && cp libz.a /opt/zlibwasm/lib/ && cp zlib.h zconf.h /opt/zlibwasm/include/

# --- libmobi + mobitool -> wasm ---
RUN mkdir -p /work && cd /work \
    && git clone https://github.com/bfabiszewski/libmobi.git && cd libmobi \
    && git checkout "${LIBMOBI_COMMIT}" \
    && ./autogen.sh \
    && ./configure --host=wasm32-wasi \
         --with-libxml2=no --disable-shared --enable-static \
         CC="$WASI/bin/clang" AR="$WASI/bin/llvm-ar" RANLIB="$WASI/bin/llvm-ranlib" \
         CFLAGS="-O2 --sysroot=$SYSROOT -I/opt/zlibwasm/include" \
         CPPFLAGS="-I/opt/zlibwasm/include" \
         LDFLAGS="--sysroot=$SYSROOT -L/opt/zlibwasm/lib" \
         LIBS="-lz" \
    && make -j"$(nproc)" \
    && mkdir -p /out && cp tools/mobitool /out/mobitool.wasm \
    && sha256sum /out/mobitool.wasm | tee /out/mobitool.wasm.sha256

# Smoke test: convert a bundled libmobi sample to EPUB using a pinned,
# checksum-verified wasmtime — NOT the `curl | bash` convenience installer, which
# is documented as interactive-only and unsafe/non-deterministic for builds — so
# the build fails loudly if the wasm can't actually convert.
ARG WASMTIME_VERSION=27.0.0
ARG WASMTIME_SHA256=74678f6ec49d8b858ca5181d5492b967245f6c072b5763ea14a75eee1ad3c2d7
RUN cd /opt \
    && curl -fsSL -o wasmtime.tar.xz \
        "https://github.com/bytecodealliance/wasmtime/releases/download/v${WASMTIME_VERSION}/wasmtime-v${WASMTIME_VERSION}-x86_64-linux.tar.xz" \
    && echo "${WASMTIME_SHA256}  wasmtime.tar.xz" | sha256sum -c - \
    && tar xJf wasmtime.tar.xz && rm wasmtime.tar.xz \
    && WASMTIME="/opt/wasmtime-v${WASMTIME_VERSION}-x86_64-linux/wasmtime" \
    && mkdir -p /smoke/out \
    && cp /work/libmobi/tests/samples/sample-ncx.mobi /smoke/in.mobi \
    && "$WASMTIME" run --dir=/smoke::/smoke /out/mobitool.wasm -e -o /smoke/out /smoke/in.mobi \
    && test -f /smoke/out/in.epub \
    && echo "SMOKE OK: $(wc -c < /smoke/out/in.epub) byte EPUB produced"

FROM scratch AS artifact
COPY --from=build /out/mobitool.wasm /mobitool.wasm
COPY --from=build /out/mobitool.wasm.sha256 /mobitool.wasm.sha256

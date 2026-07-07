//go:build wangshu_p4 && arm64

// register_arm64.go — arm64-specific init hook, mirroring the amd64
// registration in peropcode.go but without the amd64 head-op-replay
// PerOpCode (which uses jitamd64 exclusively).
//
// On arm64, PJ10 is native-only: TranslateProtoNative handles all
// accepted Protos; there is no head-op replay fallback. When
// AnalyzeNative rejects a Proto, Compile returns
// ErrCompileUnsupportedShape and the tier framework leaves the Proto
// on crescent.
package peroptranslator

import (
	"errors"

	"github.com/Liam0205/wangshu/internal/bridge"
	"github.com/Liam0205/wangshu/internal/bytecode"
	"github.com/Liam0205/wangshu/internal/gibbous/jit"
)

func init() {
	jit.RegisterPerOpTranslator(
		func(proto *bytecode.Proto, host jit.P4HostState) (bridge.GibbousCode, error) {
			if AnalyzeNative(proto) {
				code, err := TranslateProtoNative(proto, host)
				if err == nil {
					return code, nil
				}
			}
			return nil, errors.New("peroptranslator: proto not supported by arm64 native path")
		},
		func(proto *bytecode.Proto) bool {
			return AnalyzeNative(proto)
		},
	)
	jit.RegisterPerOpNativeAnalyzer(PreferNative)
	jit.RegisterPerOpSeg2SegAnalyzer(ProtoSeg2SegEligible)
}

// Logger implementation and promotion log-format assertions
// (`docs/design/p2-bridge/04-try-compile-fallback.md` §6.5 + 06-testing-strategy.md).
//
// Three log formats locked down: promoted / stays / compile failed — string-contains
// assertions (field values may change, format drift is not allowed).
package bridge

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/Liam0205/wangshu/internal/bytecode"
)

// captureLogger records all Log* calls for test assertions.
type captureLogger struct {
	promoted    []string
	stuck       []string
	compileFail []string
	panicked    []string
}

func (c *captureLogger) LogPromoted(p *bytecode.Proto, pd *ProfileData) {
	c.promoted = append(c.promoted, "promoted:"+protoName(p))
}
func (c *captureLogger) LogStuck(p *bytecode.Proto, pd *ProfileData, comp Compilability) {
	c.stuck = append(c.stuck, "stuck:"+protoName(p)+":"+comp.String())
}
func (c *captureLogger) LogCompileFail(p *bytecode.Proto, pd *ProfileData, err error) {
	c.compileFail = append(c.compileFail, "fail:"+protoName(p)+":"+err.Error())
}
func (c *captureLogger) LogPanic(p *bytecode.Proto, _ interface{}) {
	c.panicked = append(c.panicked, "panic:"+protoName(p))
}

// TestLogger_PromotedFormat: the promotion-success log contains the key phrase "promoted to gibbous".
func TestLogger_PromotedFormat(t *testing.T) {
	var buf bytes.Buffer
	b := NewBridge()
	b.SetLogger(NewStdLogger(&buf))
	b.SetP3Compiler(dummyCompileP3{})

	p := makeProtoWithCode(bytecode.ADD)
	p.Source = "test.lua"
	p.LineDefined = 42
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	got := buf.String()
	if !strings.Contains(got, "promoted to gibbous") {
		t.Errorf("missing 'promoted to gibbous' in: %q", got)
	}
	if !strings.Contains(got, "test.lua:42") {
		t.Errorf("missing proto name in: %q", got)
	}
	if !strings.Contains(got, "entry=") || !strings.Contains(got, "backedge=") {
		t.Errorf("missing entry/backedge fields in: %q", got)
	}
	if !strings.Contains(got, "feedback=") {
		t.Errorf("missing feedback field in: %q", got)
	}
}

// TestLogger_StuckFormat: the non-compilable log contains the key phrase "stays interpreted" + F<n> number.
func TestLogger_StuckFormat(t *testing.T) {
	var buf bytes.Buffer
	b := NewBridge()
	b.SetLogger(NewStdLogger(&buf))

	p := makeProtoWithCode(bytecode.ADD)
	p.Source = "stuck.lua"
	p.LineDefined = 7
	pd := b.ProfileOf(p)
	pd.Compilable = CompNotCompilable
	pd.Reasons = ReasonVararg | ReasonOverSize

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	got := buf.String()
	if !strings.Contains(got, "stays interpreted") {
		t.Errorf("missing 'stays interpreted' in: %q", got)
	}
	if !strings.Contains(got, "F1 vararg") {
		t.Errorf("missing F1 vararg reason in: %q", got)
	}
	if !strings.Contains(got, "F5 oversize") {
		t.Errorf("missing F5 oversize reason in: %q", got)
	}
	if !strings.Contains(got, "stuck.lua:7") {
		t.Errorf("missing proto name in: %q", got)
	}
}

// TestLogger_CompileFailFormat: the compile-failure log carries the WARN level + err detail.
func TestLogger_CompileFailFormat(t *testing.T) {
	var buf bytes.Buffer
	b := NewBridge()
	b.SetLogger(NewStdLogger(&buf))
	b.SetP3Compiler(failingP3{err: errors.New("oom: linear memory exceeded")})

	p := makeProtoWithCode(bytecode.ADD)
	p.Source = "fail.lua"
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	got := buf.String()
	if !strings.Contains(got, "WARN") {
		t.Errorf("missing WARN level in: %q", got)
	}
	if !strings.Contains(got, "compile failed") {
		t.Errorf("missing 'compile failed' phrase in: %q", got)
	}
	if !strings.Contains(got, "linear memory exceeded") {
		t.Errorf("missing err detail in: %q", got)
	}
}

// TestLogger_PanicFormat: the P3 panic log carries the ERROR level + stack.
func TestLogger_PanicFormat(t *testing.T) {
	var buf bytes.Buffer
	b := NewBridge()
	b.SetLogger(NewStdLogger(&buf))
	b.SetP3Compiler(panicP3{})

	p := makeProtoWithCode(bytecode.ADD)
	p.Source = "panicker.lua"
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("panic must not escape, got %v", r)
		}
	}()

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}

	got := buf.String()
	if !strings.Contains(got, "ERROR") {
		t.Errorf("missing ERROR level in: %q", got)
	}
	if !strings.Contains(got, "P3 backend panic") {
		t.Errorf("missing 'P3 backend panic' phrase in: %q", got)
	}
}

// TestLogger_CaptureViaCustom: a custom Logger can capture the promotion paths —
// verifies the state machine is wired to the Logger correctly (LogPromoted /
// LogStuck / LogCompileFail are each called on the right paths).
func TestLogger_CaptureViaCustom(t *testing.T) {
	cap := &captureLogger{}
	b := NewBridge()
	b.SetLogger(cap)
	b.SetP3Compiler(dummyCompileP3{})

	pPromoted := makeProtoWithCode(bytecode.ADD)
	pPromoted.Source = "ok.lua"
	b.ProfileOf(pPromoted).Compilable = CompCompilable

	pStuck := makeProtoWithCode(bytecode.ADD)
	pStuck.Source = "bad.lua"
	b.ProfileOf(pStuck).Compilable = CompNotCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(pPromoted, true)
		b.OnEnter(pStuck, true)
	}

	if len(cap.promoted) != 1 || !strings.Contains(cap.promoted[0], "ok.lua") {
		t.Errorf("promoted log = %v, want one entry for ok.lua", cap.promoted)
	}
	if len(cap.stuck) != 1 || !strings.Contains(cap.stuck[0], "bad.lua") {
		t.Errorf("stuck log = %v, want one entry for bad.lua", cap.stuck)
	}
}

// TestLogger_FormatReasonsAllFlags sets every reasons bit to 1 and runs once,
// verifying all shape names are translated correctly (guards against adding a
// reason bit some day and forgetting to update formatReasons).
func TestLogger_FormatReasonsAllFlags(t *testing.T) {
	all := ReasonVararg | ReasonYield | ReasonResume | ReasonCoroutine |
		ReasonUnknownCall | ReasonDebug | ReasonSetfenv |
		ReasonOverSize | ReasonOverRegs | ReasonNestedDeep |
		ReasonOverUpval | ReasonBackendUnsupp | ReasonSelfCall
	got := formatReasons(all)
	for _, want := range []string{
		"F1 vararg", "F2", "yield", "resume", "unknownCall", "selfCall",
		"F3 debug", "F4 setfenv",
		"F5", "oversize", "overregs",
		"F6", "nestedDeep", "overupval",
		"F7 backendUnsupp",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formatReasons missing %q in: %q", want, got)
		}
	}
}

// TestLogger_FeedbackSummary: the feedback field is formatted as "arith=N mono=M mega=K".
func TestLogger_FeedbackSummary(t *testing.T) {
	if got := feedbackSummary(nil); got != "nil" {
		t.Errorf("nil feedback summary = %q, want 'nil'", got)
	}
	fb := &TypeFeedback{
		Points: []PointFeedback{
			{Kind: FBArithStableNumber}, {Kind: FBArithStableNumber},
			{Kind: FBTableMono}, {Kind: FBSelfMono},
			{Kind: FBTableMega},
			{Kind: FBUnstable},
		},
	}
	got := feedbackSummary(fb)
	want := "arith=2 mono=2 mega=1"
	if got != want {
		t.Errorf("feedbackSummary = %q, want %q", got, want)
	}
}

// TestSilentLogger: the no-op Logger neither floods output nor causes any error.
func TestSilentLogger(t *testing.T) {
	b := NewBridge()
	b.SetLogger(NewSilentLogger())
	b.SetP3Compiler(dummyCompileP3{})

	p := makeProtoWithCode(bytecode.ADD)
	pd := b.ProfileOf(p)
	pd.Compilable = CompCompilable

	for i := uint32(0); i < HotEntryThreshold; i++ {
		b.OnEnter(p, true)
	}
	if pd.TierState != TierGibbous {
		t.Errorf("silent logger should not affect state machine")
	}
}

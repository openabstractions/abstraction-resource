package gpufault

import (
	"fmt"
	"maps"
	"slices"
	"sort"
)

// WordKnownBad refuses a request whose shape reaches a value recorded as
// hanging the GPU (RES-G5).
const WordKnownBad = "gpu:known-bad"

// Dispatch is one engine kind's GPU dispatch policy, as data: the
// environment it runs with, the arguments it always carries, and, per
// request-shape measure, the smallest value recorded as hanging the GPU. A
// provider applies its engine's row at every launch and checks every request
// against it (RES-G5).
type Dispatch struct {
	Engine string
	Env    map[string]string
	Args   []string
	// KnownBad maps a shape measure the adapter computes (for example
	// "math-attention-tokens") to the smallest value that hung the GPU.
	KnownBad map[string]int64
	// Fixes are engine-side settings the provider's launcher or engine
	// patch applies that no environment variable or argument carries.
	Fixes []string
	// Why is the evidence each setting rests on.
	Why string
}

// The engine kinds the rows below name.
const (
	EngineSDCpp       = "sdcpp"
	EnginePyTorchROCm = "pytorch-rocm"
)

// dispatches is every row this release knows. A row changes by adding
// evidence here, never by a provider's local patch; DispatchFor hands out
// copies, so no caller can change a row for every other.
var dispatches = []Dispatch{
	{
		Engine: EngineSDCpp,
		Args:   []string{"--diffusion-fa"},
		Why: "research/live-runs/2026-10-01.md run 3b (sd-server on Vulkan): FastWan 1280x704x121 without flash attention " +
			"reached 87 GB of commit and could not finish; run 3d with --diffusion-fa peaked at 32.8 GB. " +
			"One unfused attention over the whole clip is the long dispatch a driver watchdog ends.",
	},
	{
		Engine: EnginePyTorchROCm,
		Env: map[string]string{
			"MIOPEN_FIND_MODE":                        "FAST",
			"MIOPEN_DEBUG_CONV_DIRECT":                "0",
			// The naive reference solvers, timed at up to 6,667 ms per
			// dispatch in run b's search (stack review condition 4).
			"MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_FWD": "0",
			"MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_BWD": "0",
			"MIOPEN_DEBUG_CONV_DIRECT_NAIVE_CONV_WRW": "0",
			"MIOPEN_DEBUG_AMD_WINOGRAD_FURY_RXS_F2X3": "0",
			"MIOPEN_DEBUG_AMD_WINOGRAD_FURY_RXS_F3X2": "0",
			"TORCH_ROCM_AOTRITON_ENABLE_EXPERIMENTAL": "1",
		},
		KnownBad: map[string]int64{
			"math-attention-tokens": 27280,
		},
		Fixes: []string{
			"torch.backends.cudnn.enabled=False: convolutions run on hipBLASLt and rocBLAS GEMMs and MIOpen searches nothing",
			"Hummingbird-XT WanDiffusionWrapper.seq_len from the clip (latent frames x H/32 x W/32), never the constant 27280",
		},
		Why: "research/live-runs/2026-10-01-rocm.md, Radeon 8060S, gfx1151 Windows wheel with no MIOpen database: " +
			"run b, the FLUX.2 [klein] VAE decode's MIOpen live solver search in Modern Standby, ended in WerKernel " +
			"WATCHDOG 20:41:36 and AMD_WATCHDOG 20:43:59; run b' with MIOpen convolution off, FIND_MODE=FAST, " +
			"CONV_DIRECT=0 and the Winograd Fury solvers off (ROCm/TheRock#4954) passed. Run c, Hummingbird-XT " +
			"512x512x81 padded to 27280 tokens on the SDPA math fallback, ran 172 s for step 1 and ended in WATCHDOG " +
			"21:08:32 in step 2; runs c' and c'' with seq_len from the clip and AOTriton's experimental kernels passed.",
	},
}

// DispatchFor is a copy of the row for engine, or false.
func DispatchFor(engine string) (Dispatch, bool) {
	for _, d := range dispatches {
		if d.Engine == engine {
			d.Env = maps.Clone(d.Env)
			d.Args = slices.Clone(d.Args)
			d.KnownBad = maps.Clone(d.KnownBad)
			d.Fixes = slices.Clone(d.Fixes)
			return d, true
		}
	}
	return Dispatch{}, false
}

// WithArgs is args with every argument of the row the list lacks appended.
func (d Dispatch) WithArgs(args []string) []string {
	out := append([]string(nil), args...)
	for _, a := range d.Args {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// Environment is the row's environment as NAME=value items, sorted.
func (d Dispatch) Environment() []string {
	out := make([]string, 0, len(d.Env))
	for name, value := range d.Env {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}

// Check refuses a request whose shape reaches a known-bad value. A measure
// the adapter did not compute is not checked.
func (d Dispatch) Check(shape map[string]int64) error {
	names := make([]string, 0, len(d.KnownBad))
	for name := range d.KnownBad {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value, ok := shape[name]
		if ok && value >= d.KnownBad[name] {
			return &Refusal{Word: WordKnownBad, Detail: fmt.Sprintf("%s %d reaches %d, which hung the GPU on %s", name, value, d.KnownBad[name], d.Engine)}
		}
	}
	return nil
}

package instrument

import (
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The adapter's GPU-visible memory and one bound child's use, both read from
// the graphics kernel's own figures (CONTRACT.md RES-T2, RES-L2).
//
// On the owner's Radeon 8060S (128 GiB installed, a 4 GiB UMA carve-out,
// 123.6 GiB visible to Windows) DXGI states DedicatedVideoMemory 4 GiB and
// SharedSystemMemory half the visible memory: the GPU can map at most their
// sum. Physical memory alone overstated the card by about 58 GiB, and an
// unknown card made the model host plan the smallest context.
var (
	dxgi               = windows.NewLazySystemDLL("dxgi.dll")
	createDXGIFactory1 = dxgi.NewProc("CreateDXGIFactory1")

	gdi32                 = windows.NewLazySystemDLL("gdi32.dll")
	kmtEnumAdapters2      = gdi32.NewProc("D3DKMTEnumAdapters2")
	kmtQueryVideoMemory   = gdi32.NewProc("D3DKMTQueryVideoMemoryInfo")
	kmtCloseAdapter       = gdi32.NewProc("D3DKMTCloseAdapter")
	iidIDXGIFactory1      = windows.GUID{Data1: 0x770aae78, Data2: 0xf26f, Data3: 0x4dba, Data4: [8]byte{0xa8, 0x29, 0x25, 0x3c, 0x83, 0xd1, 0xb3, 0x87}}
	errAdapterUnavailable = errors.New("no hardware graphics adapter")
)

const (
	dxgiErrorNotFound   = 0x887A0002
	dxgiAdapterSoftware = 0x2
	// IDXGIFactory1::EnumAdapters1 and IDXGIAdapter1::GetDesc1 vtable slots.
	vtblEnumAdapters1 = 12
	vtblGetDesc1      = 10
	vtblRelease       = 2
	stillActive       = 259
)

// dxgiAdapterDesc1 is DXGI_ADAPTER_DESC1.
type dxgiAdapterDesc1 struct {
	Description           [128]uint16
	VendorID              uint32
	DeviceID              uint32
	SubSysID              uint32
	Revision              uint32
	DedicatedVideoMemory  uintptr
	DedicatedSystemMemory uintptr
	SharedSystemMemory    uintptr
	AdapterLuid           windows.LUID
	Flags                 uint32
}

// adapterVisible is one hardware adapter's GPU-visible bytes: its dedicated
// video memory, the system memory dedicated to it and the system memory it
// may share. Software adapters (the basic render driver) are left out.
func adapterVisible(desc dxgiAdapterDesc1) int64 {
	if desc.Flags&dxgiAdapterSoftware != 0 {
		return 0
	}
	total := uint64(desc.DedicatedVideoMemory) + uint64(desc.DedicatedSystemMemory) + uint64(desc.SharedSystemMemory)
	if total > 1<<62 {
		return 0
	}
	return int64(total)
}

// comObject is a COM interface pointer's target: its first word is the
// vtable. Only the slots named above are ever read.
type comObject struct{ vtbl *[16]uintptr }

// call calls slot of the object's vtable with the object as the first
// argument and returns the HRESULT. An address in args must come from
// outParam: call may grow the goroutine's stack after the address became a
// uintptr, and a stack variable would move from under the COM method.
func (o *comObject) call(slot int, args ...uintptr) uint32 {
	r, _, _ := syscall.SyscallN(o.vtbl[slot], append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)...)
	return uint32(r)
}

// outParam allocates a COM method's out parameter on the heap, which never
// moves, and keeps it pinned until pin is unpinned.
func outParam[T any](pin *runtime.Pinner) *T {
	p := new(T)
	pin.Pin(p)
	return p
}

// AdapterCapacity is the GPU-visible bytes of the largest hardware graphics
// adapter, read from DXGI's adapter description: the operating system's fixed
// figure for the adapter, never the per-process budget QueryVideoMemoryInfo
// answers. Zero with an error when no hardware adapter answers.
func AdapterCapacity() (int64, error) {
	bytes, _, err := card0Adapter()
	return bytes, err
}

// card0Adapter is card:0: the largest hardware graphics adapter, its
// GPU-visible bytes and its LUID. The first adapter DXGI enumerates wins a
// tie. The LUID names the adapter's PDH rows for this boot only.
func card0Adapter() (int64, windows.LUID, error) {
	if err := createDXGIFactory1.Find(); err != nil {
		return 0, windows.LUID{}, err
	}
	var pin runtime.Pinner
	defer pin.Unpin()
	out := outParam[*comObject](&pin)
	hr, _, _ := createDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(out))) //unchecked: COM returns the checked HRESULT in hr, not GetLastError
	factory := *out
	if uint32(hr) != 0 || factory == nil {
		return 0, windows.LUID{}, fmt.Errorf("CreateDXGIFactory1: 0x%08x", uint32(hr))
	}
	defer factory.call(vtblRelease)
	var descs []dxgiAdapterDesc1
	for index := uintptr(0); index < 64; index++ {
		slot := outParam[*comObject](&pin)
		hr := factory.call(vtblEnumAdapters1, index, uintptr(unsafe.Pointer(slot)))
		adapter := *slot
		if hr == dxgiErrorNotFound {
			break
		}
		if hr != 0 || adapter == nil {
			return 0, windows.LUID{}, fmt.Errorf("EnumAdapters1(%d): 0x%08x", index, hr)
		}
		desc := outParam[dxgiAdapterDesc1](&pin)
		hr = adapter.call(vtblGetDesc1, uintptr(unsafe.Pointer(desc)))
		adapter.call(vtblRelease)
		if hr != 0 {
			return 0, windows.LUID{}, fmt.Errorf("GetDesc1(%d): 0x%08x", index, hr)
		}
		descs = append(descs, *desc)
	}
	return largestAdapter(descs)
}

// largestAdapter picks card:0 from DXGI's adapter descriptions.
func largestAdapter(descs []dxgiAdapterDesc1) (int64, windows.LUID, error) {
	var best int64
	var luid windows.LUID
	for _, desc := range descs {
		if visible := adapterVisible(desc); visible > best {
			best, luid = visible, desc.AdapterLuid
		}
	}
	if best <= 0 {
		return 0, windows.LUID{}, errAdapterUnavailable
	}
	return best, luid, nil
}

// kmtAdapterInfo is D3DKMT_ADAPTERINFO.
type kmtAdapterInfo struct {
	Adapter uint32
	Luid    windows.LUID
	Sources uint32
	Precise int32
}

// kmtEnumAdapters is D3DKMT_ENUMADAPTERS2.
type kmtEnumAdapters struct {
	Count    uint32
	Adapters *kmtAdapterInfo
}

// kmtVideoMemoryInfo is D3DKMT_QUERYVIDEOMEMORYINFO.
type kmtVideoMemoryInfo struct {
	Process     windows.Handle
	Adapter     uint32
	Group       uint32
	Budget      uint64
	Usage       uint64
	Reservation uint64
	Available   uint64
	Physical    uint32
}

// ChildUsage measures one process's graphics memory through a handle on that
// exact process: the graphics kernel's current usage of both memory segment
// groups on every adapter it enumerates. The handle is held across the whole
// read, so its PID cannot be reused meanwhile; the process's creation time
// must equal startID before the read and the process must still run after
// it. Any failure, an adapter with linked physical adapters included,
// withholds the whole figure (CONTRACT.md RES-L2).
func ChildUsage(pid int, startID string) (int64, error) {
	if pid <= 0 || startID == "" {
		return 0, errors.New("child usage needs a process identity")
	}
	for _, proc := range []*windows.LazyProc{kmtEnumAdapters2, kmtQueryVideoMemory, kmtCloseAdapter} {
		if err := proc.Find(); err != nil {
			return 0, err
		}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		if h, err = windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid)); err != nil {
			return 0, err
		}
	}
	defer windows.CloseHandle(h)
	same := func() error {
		var creation, exit, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
			return err
		}
		if strconv.FormatInt(creation.Nanoseconds(), 10) != startID {
			return errors.New("process creation identity changed")
		}
		var code uint32
		if err := windows.GetExitCodeProcess(h, &code); err != nil {
			return err
		}
		if code != stillActive {
			return errors.New("process exited")
		}
		return nil
	}
	if err := same(); err != nil {
		return 0, err
	}
	var enum kmtEnumAdapters
	if status, _, _ := kmtEnumAdapters2.Call(uintptr(unsafe.Pointer(&enum))); status != 0 {
		return 0, fmt.Errorf("D3DKMTEnumAdapters2: 0x%08x", uint32(status))
	}
	if enum.Count == 0 || enum.Count > 64 {
		return 0, errAdapterUnavailable
	}
	adapters := make([]kmtAdapterInfo, enum.Count)
	enum.Adapters = &adapters[0]
	if status, _, _ := kmtEnumAdapters2.Call(uintptr(unsafe.Pointer(&enum))); status != 0 {
		return 0, fmt.Errorf("D3DKMTEnumAdapters2: 0x%08x", uint32(status))
	}
	adapters = adapters[:enum.Count]
	defer func() {
		for _, a := range adapters {
			closing := struct{ Adapter uint32 }{a.Adapter}
			//unchecked: a close failure leaves nothing this read can repair; the figure was already taken
			kmtCloseAdapter.Call(uintptr(unsafe.Pointer(&closing)))
		}
	}()
	query := func(adapter, group, physical uint32) (uint64, bool) {
		info := kmtVideoMemoryInfo{Process: h, Adapter: adapter, Group: group, Physical: physical}
		status, _, _ := kmtQueryVideoMemory.Call(uintptr(unsafe.Pointer(&info)))
		return info.Usage, status == 0
	}
	var total uint64
	for _, a := range adapters {
		if _, linked := query(a.Adapter, 0, 1); linked {
			return 0, errors.New("linked physical adapters are not measured")
		}
		for group := uint32(0); group < 2; group++ {
			usage, ok := query(a.Adapter, group, 0)
			if !ok {
				return 0, fmt.Errorf("D3DKMTQueryVideoMemoryInfo failed for an adapter")
			}
			total += usage
		}
	}
	if err := same(); err != nil {
		return 0, err
	}
	if total > 1<<62 {
		return 0, errors.New("child usage out of range")
	}
	return int64(total), nil
}

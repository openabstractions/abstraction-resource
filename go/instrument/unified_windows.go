package instrument

import (
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// AdapterUnified reports whether card:0, the largest hardware graphics
// adapter (AdapterCapacity), draws its memory from the machine's RAM: Direct3D
// 12's architecture answer UMA for that adapter. A discrete card answers
// false. An error says the answer is unknown. The answer is read once per
// process.
func AdapterUnified() (bool, error) {
	unifiedOnce.Do(func() { unified, unifiedErr = adapterUnified() })
	return unified, unifiedErr
}

var (
	unifiedOnce sync.Once
	unified     bool
	unifiedErr  error

	d3d12             = windows.NewLazySystemDLL("d3d12.dll")
	d3d12CreateDevice = d3d12.NewProc("D3D12CreateDevice")
	iidID3D12Device   = windows.GUID{Data1: 0x189819f1, Data2: 0x1db6, Data3: 0x4b57, Data4: [8]byte{0xbe, 0x54, 0x18, 0x21, 0x33, 0x9b, 0x85, 0xf7}}
)

const (
	// D3D_FEATURE_LEVEL_11_0, the lowest level D3D12 creates a device at.
	d3dFeatureLevel11 = 0xb000
	// D3D12_FEATURE_ARCHITECTURE and ID3D12Device::CheckFeatureSupport's
	// vtable slot (IUnknown 3, ID3D12Object 4, then GetNodeCount ..
	// CreateCommandList).
	d3d12FeatureArchitecture = 1
	vtblCheckFeatureSupport  = 13
)

// d3d12Architecture is D3D12_FEATURE_DATA_ARCHITECTURE.
type d3d12Architecture struct {
	NodeIndex         uint32
	TileBasedRenderer int32
	UMA               int32
	CacheCoherentUMA  int32
}

func adapterUnified() (bool, error) {
	if err := createDXGIFactory1.Find(); err != nil {
		return false, err
	}
	if err := d3d12CreateDevice.Find(); err != nil {
		return false, err
	}
	var factory *comObject
	if hr, _, _ := createDXGIFactory1.Call(uintptr(unsafe.Pointer(&iidIDXGIFactory1)), uintptr(unsafe.Pointer(&factory))); uint32(hr) != 0 || factory == nil {
		return false, fmt.Errorf("CreateDXGIFactory1: 0x%08x", uint32(hr))
	}
	defer factory.call(vtblRelease)
	var best *comObject
	var bestSize int64
	for index := uintptr(0); index < 64; index++ {
		var adapter *comObject
		hr := factory.call(vtblEnumAdapters1, index, uintptr(unsafe.Pointer(&adapter)))
		if hr == dxgiErrorNotFound {
			break
		}
		if hr != 0 || adapter == nil {
			return false, fmt.Errorf("EnumAdapters1(%d): 0x%08x", index, hr)
		}
		var desc dxgiAdapterDesc1
		if hr := adapter.call(vtblGetDesc1, uintptr(unsafe.Pointer(&desc))); hr != 0 {
			adapter.call(vtblRelease)
			return false, fmt.Errorf("GetDesc1(%d): 0x%08x", index, hr)
		}
		if size := adapterVisible(desc); size > bestSize {
			if best != nil {
				best.call(vtblRelease)
			}
			best, bestSize = adapter, size
			continue
		}
		adapter.call(vtblRelease)
	}
	if best == nil {
		return false, errAdapterUnavailable
	}
	defer best.call(vtblRelease)
	var device *comObject
	hr, _, _ := d3d12CreateDevice.Call(uintptr(unsafe.Pointer(best)), d3dFeatureLevel11,
		uintptr(unsafe.Pointer(&iidID3D12Device)), uintptr(unsafe.Pointer(&device)))
	if uint32(hr) != 0 || device == nil {
		return false, fmt.Errorf("D3D12CreateDevice: 0x%08x", uint32(hr))
	}
	defer device.call(vtblRelease)
	var arch d3d12Architecture
	if hr := device.call(vtblCheckFeatureSupport, d3d12FeatureArchitecture, uintptr(unsafe.Pointer(&arch)), unsafe.Sizeof(arch)); hr != 0 {
		return false, fmt.Errorf("CheckFeatureSupport(ARCHITECTURE): 0x%08x", hr)
	}
	return arch.UMA != 0, nil
}

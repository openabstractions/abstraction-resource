package gpufault

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The console display state, read through a power setting notification
// (powrprof PowerSettingRegisterNotification with a callback, so no window is
// needed). Windows reports the current state once on registration and every
// change after it. On a Modern Standby machine standby begins only after the
// display turns off, so a display that is on or dimmed is a machine that is
// awake (RES-G5).
var (
	powrprof                             = windows.NewLazySystemDLL("powrprof.dll")
	procPowerSettingRegisterNotification = powrprof.NewProc("PowerSettingRegisterNotification")

	// guidConsoleDisplayState is GUID_CONSOLE_DISPLAY_STATE,
	// {6FE69556-704A-47A0-8F24-C28D936FDA47}.
	guidConsoleDisplayState = windows.GUID{Data1: 0x6fe69556, Data2: 0x704a, Data3: 0x47a0,
		Data4: [8]byte{0x8f, 0x24, 0xc2, 0x8d, 0x93, 0x6f, 0xda, 0x47}}
)

const (
	deviceNotifyCallback   = 2
	pbtPowerSettingChange  = 0x8013
	displayStateFirstWait  = 500 * time.Millisecond
	displayStateOff        = 0
	displayStateOn         = 1
	displayStateDimmed     = 2
	displayStateUnreported = -1
)

// deviceNotifySubscribeParameters is DEVICE_NOTIFY_SUBSCRIBE_PARAMETERS.
type deviceNotifySubscribeParameters struct {
	callback uintptr
	context  uintptr
}

// powerBroadcastSetting is the head of POWERBROADCAST_SETTING; Data follows.
type powerBroadcastSetting struct {
	setting    windows.GUID
	dataLength uint32
	data       uint32
}

var consoleDisplay struct {
	once     sync.Once
	err      error
	state    atomic.Int32
	reported chan struct{}
	first    sync.Once
	params   deviceNotifySubscribeParameters
	handle   uintptr
	// firstBy is when the registration's first report is due; a read after
	// it does not wait.
	firstBy time.Time
}

func init() { consoleDisplay.state.Store(displayStateUnreported) }

// startDisplayWatch registers for the console display state once per process.
func startDisplayWatch() error {
	consoleDisplay.once.Do(func() {
		consoleDisplay.reported = make(chan struct{})
		consoleDisplay.firstBy = time.Now().Add(displayStateFirstWait)
		if err := powrprof.Load(); err != nil {
			consoleDisplay.err = err
			return
		}
		consoleDisplay.params.callback = windows.NewCallback(displayChanged)
		r, _, _ := procPowerSettingRegisterNotification.Call(
			uintptr(unsafe.Pointer(&guidConsoleDisplayState)), deviceNotifyCallback,
			uintptr(unsafe.Pointer(&consoleDisplay.params)), uintptr(unsafe.Pointer(&consoleDisplay.handle)))
		if r != 0 {
			consoleDisplay.err = fmt.Errorf("PowerSettingRegisterNotification: %w", windows.Errno(r))
		}
	})
	return consoleDisplay.err
}

// displayChanged is the DEVICE_NOTIFY_CALLBACK_ROUTINE.
func displayChanged(_ uintptr, kind uintptr, setting *powerBroadcastSetting) uintptr {
	if kind != pbtPowerSettingChange || setting == nil {
		return 0
	}
	if setting.setting != guidConsoleDisplayState || setting.dataLength < 4 {
		return 0
	}
	consoleDisplay.state.Store(int32(setting.data))
	consoleDisplay.first.Do(func() { close(consoleDisplay.reported) })
	return 0
}

// PlatformAwake reports whether the machine is demonstrably awake now: the
// console display is on or dimmed. An error is a state that cannot be read;
// the Guard then admits the work and logs one line (RES-G5).
func PlatformAwake() (bool, error) {
	if err := startDisplayWatch(); err != nil {
		return false, err
	}
	if !displayReported(time.Until(consoleDisplay.firstBy)) {
		return false, errors.New("Windows has not reported the display state")
	}
	switch consoleDisplay.state.Load() {
	case displayStateOn, displayStateDimmed:
		return true, nil
	case displayStateOff:
		return false, nil
	default:
		return false, fmt.Errorf("the display state %d is unknown", consoleDisplay.state.Load())
	}
}

// displayReported reports whether Windows has reported the display state,
// waiting up to wait for its first report.
func displayReported(wait time.Duration) bool {
	select {
	case <-consoleDisplay.reported:
		return true
	default:
	}
	if wait <= 0 {
		return false
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-consoleDisplay.reported:
		return true
	case <-timer.C:
		return false
	}
}

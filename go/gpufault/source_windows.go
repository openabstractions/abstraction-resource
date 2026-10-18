package gpufault

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The Windows Event Log API, bound here because golang.org/x/sys/windows
// carries no wevtapi wrappers. A subscription is pulled: the service sets a
// manual-reset event when records arrive and EvtNext reads them.
var (
	wevtapi          = windows.NewLazySystemDLL("wevtapi.dll")
	procEvtSubscribe = wevtapi.NewProc("EvtSubscribe")
	procEvtQuery     = wevtapi.NewProc("EvtQuery")
	procEvtNext      = wevtapi.NewProc("EvtNext")
	procEvtRender    = wevtapi.NewProc("EvtRender")
	procEvtClose     = wevtapi.NewProc("EvtClose")
)

const (
	evtSubscribeToFutureEvents = 1
	// evtSubscribeTolerateQueryErrors keeps the other channels of the query
	// when one of them, such as WerKernel/Operational, does not exist.
	evtSubscribeTolerateQueryErrors = 0x1000
	evtQueryChannelPath             = 0x1
	evtQueryReverseDirection        = 0x200
	evtRenderEventXML               = 1
	evtNextBatch                    = 16
	evtNextTimeoutMs                = 1000
	waitPollMs                      = 500
)

// PlatformRecords is this platform's records: on Windows the System,
// WerKernel/Operational and Application logs (QueryList).
func PlatformRecords() Records { return windowsSource{} }

type windowsSource struct{}

// Watch delivers Watching once its subscription stands, the standby state at
// start (startupStandby), then every matching record from now on, until ctx
// ends. An EvtNext failure other than no more records or a timeout returns,
// and the Guard subscribes again.
func (windowsSource) Watch(ctx context.Context, deliver func(Event)) error {
	if err := wevtapi.Load(); err != nil {
		return err
	}
	signal, err := windows.CreateEvent(nil, 1, 1, nil)
	if err != nil {
		return fmt.Errorf("CreateEvent: %w", err)
	}
	defer windows.CloseHandle(signal)
	query, err := windows.UTF16PtrFromString(QueryList)
	if err != nil {
		return err
	}
	sub, _, callErr := procEvtSubscribe.Call(0, uintptr(signal), 0, uintptr(unsafe.Pointer(query)), 0, 0, 0,
		evtSubscribeToFutureEvents|evtSubscribeTolerateQueryErrors)
	if sub == 0 {
		return fmt.Errorf("EvtSubscribe: %w", callErr)
	}
	defer evtClose(sub)
	deliver(Event{Kind: Watching, At: time.Now(), Source: "EvtSubscribe"})
	last, haveLast := newest(StandbyQuery)
	boot, haveBoot := newest(bootQuery)
	if !haveBoot || boot.Kind != bootRecord {
		boot = Event{}
	}
	if ev, ok := startupStandby(last, haveLast, boot.At); ok {
		deliver(ev)
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		state, err := windows.WaitForSingleObject(signal, waitPollMs)
		if err != nil {
			return fmt.Errorf("WaitForSingleObject: %w", err)
		}
		if state != windows.WAIT_OBJECT_0 {
			continue
		}
		// Reset before reading, so a record that arrives during the read
		// signals again rather than waiting for the next one.
		if err := windows.ResetEvent(signal); err != nil {
			return fmt.Errorf("ResetEvent: %w", err)
		}
		if err := drain(sub, deliver); err != nil {
			return err
		}
	}
}

// drain reads every record the result set holds now.
func drain(set uintptr, deliver func(Event)) error {
	for {
		var handles [evtNextBatch]uintptr
		var returned uint32
		r, _, err := procEvtNext.Call(set, evtNextBatch, uintptr(unsafe.Pointer(&handles[0])), evtNextTimeoutMs, 0, uintptr(unsafe.Pointer(&returned)))
		if r == 0 {
			if errors.Is(err, windows.ERROR_NO_MORE_ITEMS) || errors.Is(err, windows.Errno(windows.WAIT_TIMEOUT)) {
				return nil
			}
			return fmt.Errorf("EvtNext: %w", err)
		}
		for _, h := range handles[:returned] {
			rendered, renderErr := render(h)
			evtClose(h)
			if renderErr != nil {
				continue
			}
			if ev, ok := FromXML(rendered); ok {
				deliver(ev)
			}
		}
	}
}

// newest is the newest System log record xpath selects that FromXML
// accepts.
func newest(xpath string) (Event, bool) {
	path, err := windows.UTF16PtrFromString(channelSystem)
	if err != nil {
		return Event{}, false
	}
	query, err := windows.UTF16PtrFromString(xpath)
	if err != nil {
		return Event{}, false
	}
	set, _, _ := procEvtQuery.Call(0, uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(query)), evtQueryChannelPath|evtQueryReverseDirection)
	if set == 0 {
		return Event{}, false
	}
	defer evtClose(set)
	var h uintptr
	var returned uint32
	if r, _, _ := procEvtNext.Call(set, 1, uintptr(unsafe.Pointer(&h)), evtNextTimeoutMs, 0, uintptr(unsafe.Pointer(&returned))); r == 0 || returned != 1 {
		return Event{}, false
	}
	defer evtClose(h)
	rendered, err := render(h)
	if err != nil {
		return Event{}, false
	}
	return FromXML(rendered)
}

// render is one record as its XML text.
func render(h uintptr) ([]byte, error) {
	var used, count uint32
	r, _, err := procEvtRender.Call(0, h, evtRenderEventXML, 0, 0, uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&count)))
	if r == 0 && !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, fmt.Errorf("EvtRender: %w", err)
	}
	if used == 0 {
		return nil, errors.New("EvtRender: empty record")
	}
	buf := make([]uint16, (used+1)/2)
	r, _, err = procEvtRender.Call(0, h, evtRenderEventXML, uintptr(len(buf)*2), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&used)), uintptr(unsafe.Pointer(&count)))
	if r == 0 {
		return nil, fmt.Errorf("EvtRender: %w", err)
	}
	return []byte(windows.UTF16ToString(buf)), nil
}

func evtClose(h uintptr) {
	if h != 0 {
		//unchecked: closing a record or result handle; nothing further depends on its outcome
		procEvtClose.Call(h)
	}
}

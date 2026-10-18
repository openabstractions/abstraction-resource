package gpufault

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/openabstractions/abstraction-resource/go/instrument"
)

// The Windows records GPU fault handling reads, all readable without
// elevation (RES-G1). Measured on the owner's Strix Halo on 2026-10-01: each
// of the day's three hangs wrote WerKernel 1001 (WATCHDOG or AMD_WATCHDOG)
// first, then the Application log's LiveKernelEvent 141 or a1000001 about
// ten seconds later; none wrote a display 4101. Kernel-Power 506 and 507
// bracketed each Modern Standby.
//
// Who can write each channel decides what a record may do. The System and
// WerKernel/Operational channels give interactive users read access alone
// (`wevtutil gl`: IU 0x1); the Application channel gives interactive users
// and every AppContainer write access (IU 0x3, S-1-15-2-1 0x2), so any
// program can write a record there that reads like Windows Error Reporting's.
// A reset is therefore triggered only by a record from System or
// WerKernel/Operational whose provider GUID is the expected one and whose
// writer is the kernel: UserID S-1-5-18, and for WerKernel process 4. The
// Application log's LiveKernelEvent and the Display 4101 record, which
// carries no provider GUID to check, only corroborate a reset already
// triggered.
const (
	channelSystem   = "System"
	channelWer      = "Microsoft-Windows-WerKernel/Operational"
	channelApp      = "Application"
	providerPower   = "Microsoft-Windows-Kernel-Power"
	providerGeneral = "Microsoft-Windows-Kernel-General"
	providerDisplay = "Display"
	providerWer     = "Microsoft-Windows-WerKernel"
	providerWerApp  = "Windows Error Reporting"

	guidPower   = "{331c3b3a-2005-44c2-ac5e-77220c37d6b4}"
	guidGeneral = "{a68ca8b7-004f-d7b6-a698-07e2de0f1f5d}"
	guidWer     = "{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}"
	localSystem = "S-1-5-18"
	kernelPID   = "4"
)

// QueryList is the one structured query a Windows source subscribes with.
const QueryList = `<QueryList><Query Id="0">` +
	`<Select Path="System">*[System[(Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=506 or EventID=507)) or (Provider[@Name='Display'] and EventID=4101)]]</Select>` +
	`<Select Path="Microsoft-Windows-WerKernel/Operational">*[System[EventID=1001]]</Select>` +
	`<Select Path="Application">*[System[Provider[@Name='Windows Error Reporting'] and EventID=1001]]</Select>` +
	`</Query></QueryList>`

// StandbyQuery finds the last Modern Standby entry or exit in the System log;
// bootQuery finds the kernel's record of the last boot (Kernel-General 12).
const (
	StandbyQuery = `*[System[Provider[@Name='Microsoft-Windows-Kernel-Power'] and (EventID=506 or EventID=507)]]`
	bootQuery    = `*[System[Provider[@Name='Microsoft-Windows-Kernel-General'] and EventID=12]]`
)

type eventXML struct {
	System struct {
		Provider struct {
			Name string `xml:"Name,attr"`
			Guid string `xml:"Guid,attr"`
		} `xml:"Provider"`
		EventID     int `xml:"EventID"`
		TimeCreated struct {
			SystemTime string `xml:"SystemTime,attr"`
		} `xml:"TimeCreated"`
		Execution struct {
			ProcessID string `xml:"ProcessID,attr"`
		} `xml:"Execution"`
		Channel  string `xml:"Channel"`
		Security struct {
			UserID string `xml:"UserID,attr"`
		} `xml:"Security"`
	} `xml:"System"`
	Data []struct {
		Name  string `xml:"Name,attr"`
		Value string `xml:",chardata"`
	} `xml:"EventData>Data"`
}

// kernelWrote reports whether a record carries the expected provider GUID,
// the expected channel and LocalSystem as its writer, and, when pid is
// set, the kernel's process.
func (e eventXML) kernelWrote(channel, guid, pid string) bool {
	s := e.System
	return s.Channel == channel && strings.EqualFold(s.Provider.Guid, guid) &&
		s.Security.UserID == localSystem && (pid == "" || s.Execution.ProcessID == pid)
}

// liveKernelResets are the LiveKernelEvent codes of a GPU timeout:
// VIDEO_ENGINE_TIMEOUT_DETECTED (141) and VIDEO_TDR_TIMEOUT_DETECTED (117).
var liveKernelResets = map[string]bool{"141": true, "117": true}

// FromXML reads one rendered Windows event and reports the Event it is, or
// false for a record that is no GPU reset and no standby change: a WerKernel
// dump of another component, a LiveKernelEvent of another code, or a record
// whose channel, provider GUID or writer is not the kernel's.
func FromXML(rendered []byte) (Event, bool) {
	var e eventXML
	if err := xml.Unmarshal(rendered, &e); err != nil {
		return Event{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, e.System.TimeCreated.SystemTime)
	if err != nil {
		return Event{}, false
	}
	data := map[string]string{}
	for _, d := range e.Data {
		data[d.Name] = strings.TrimSpace(d.Value)
	}
	provider, id := e.System.Provider.Name, e.System.EventID
	switch {
	case provider == providerPower && id == 506 && e.kernelWrote(channelSystem, guidPower, ""):
		return Event{Kind: StandbyEnter, At: at, Source: "Kernel-Power 506"}, true
	case provider == providerPower && id == 507 && e.kernelWrote(channelSystem, guidPower, ""):
		return Event{Kind: StandbyExit, At: at, Source: "Kernel-Power 507"}, true
	case provider == providerGeneral && id == 12 && e.kernelWrote(channelSystem, guidGeneral, kernelPID):
		return Event{Kind: bootRecord, At: at, Source: "Kernel-General 12"}, true
	case provider == providerWer && id == 1001 && e.kernelWrote(channelWer, guidWer, kernelPID):
		component := data["ComponentName"]
		if component == "WATCHDOG" || strings.HasSuffix(component, "_WATCHDOG") {
			return Event{Kind: Reset, At: at, Source: "WerKernel 1001 " + component}, true
		}
	case provider == providerDisplay && id == 4101 && e.System.Channel == channelSystem:
		return Event{Kind: ResetCorroboration, At: at, Source: "Display 4101"}, true
	case provider == providerWerApp && id == 1001 && e.System.Channel == channelApp:
		if data["EventName"] == "LiveKernelEvent" && liveKernelResets[strings.ToLower(data["P1"])] {
			return Event{Kind: ResetCorroboration, At: at, Source: "LiveKernelEvent " + data["P1"]}, true
		}
	}
	return Event{}, false
}

// startupStandby is the standby state a source reports when it starts: the
// last Kernel-Power 506 or 507, set aside when it is a 506 written before
// the last boot (an entry whose exit the shutdown never wrote) or when the
// boot time is unknown. A 507 is always reported; it can only admit loads.
func startupStandby(last Event, haveLast bool, boot time.Time) (Event, bool) {
	if !haveLast {
		return Event{}, false
	}
	if last.Kind == StandbyEnter && (boot.IsZero() || last.At.Before(boot)) {
		return Event{}, false
	}
	return last, true
}

// AdapterProbe is the default probe after a reset: the platform lists a
// graphics adapter again (instrument.GraphicsAdapters). The engine's own readiness,
// which the holder waits for on the next load, is the second half (RES-G4).
func AdapterProbe(context.Context) error {
	names, err := instrument.GraphicsAdapters()
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return ErrNoAdapter
	}
	return nil
}

// errNoSource is a platform with no records this package reads.
var errNoSource = fmt.Errorf("gpufault: this platform has no GPU reset records this package reads")

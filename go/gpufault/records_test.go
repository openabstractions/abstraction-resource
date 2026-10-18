package gpufault

import (
	"testing"
	"time"
)

// Records as the owner's machine rendered them on 2026-10-01 (EventData cut
// to the fields read; the computer name replaced). The forged-style records
// are written here only, as test input; none is written to a real log.
const (
	werWatchdog = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel' Guid='{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}'/><EventID>1001</EventID><Version>0</Version><Level>4</Level><Task>1</Task><Opcode>0</Opcode><Keywords>0x8000000000000001</Keywords><TimeCreated SystemTime='2026-10-01T18:08:41.1322011Z'/><EventRecordID>117</EventRecordID><Correlation/><Execution ProcessID='4' ThreadID='2092'/><Channel>Microsoft-Windows-WerKernel/Operational</Channel><Computer>machine</Computer><Security UserID='S-1-5-18'/></System><EventData><Data Name='ComponentName'>WATCHDOG</Data><Data Name='RequestedPolicy'>1</Data><Data Name='GrantedPolicy'>1</Data><Data Name='Status'>0</Data><Data Name='ThrottleCheckResult'>0</Data></EventData></Event>`
	werAMD      = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel' Guid='{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T17:43:59.5000000Z'/><Execution ProcessID='4' ThreadID='2092'/><Channel>Microsoft-Windows-WerKernel/Operational</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='ComponentName'>AMD_WATCHDOG</Data></EventData></Event>`
	werOther    = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel' Guid='{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T17:43:59.5000000Z'/><Execution ProcessID='4'/><Channel>Microsoft-Windows-WerKernel/Operational</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='ComponentName'>USBXHCI</Data></EventData></Event>`
	liveKernel  = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Windows Error Reporting' Guid='{0ead09bd-2157-539a-8d6d-c87f95b64d70}'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T18:08:51.0781814Z'/><Execution ProcessID='26596' ThreadID='9444'/><Channel>Application</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='Bucket'></Data><Data Name='BucketType'>0</Data><Data Name='EventName'>LiveKernelEvent</Data><Data Name='Response'>Not available</Data><Data Name='CabId'>0</Data><Data Name='P1'>141</Data></EventData></Event>`
	liveVendor  = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Windows Error Reporting'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T17:44:00.0000000Z'/><Channel>Application</Channel></System><EventData><Data Name='EventName'>LiveKernelEvent</Data><Data Name='P1'>a1000001</Data></EventData></Event>`
	standbyIn   = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Kernel-Power' Guid='{331c3b3a-2005-44c2-ac5e-77220c37d6b4}'/><EventID>506</EventID><Version>3</Version><Level>4</Level><Task>157</Task><TimeCreated SystemTime='2026-10-01T17:25:00.2916013Z'/><Execution ProcessID='4' ThreadID='35444'/><Channel>System</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='Reason'>12</Data></EventData></Event>`
	standbyOut  = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Kernel-Power' Guid='{331c3b3a-2005-44c2-ac5e-77220c37d6b4}'/><EventID>507</EventID><TimeCreated SystemTime='2026-10-01T17:42:55.0000000Z'/><Execution ProcessID='4'/><Channel>System</Channel><Security UserID='S-1-5-18'/></System></Event>`
	bootRecordX = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Kernel-General' Guid='{a68ca8b7-004f-d7b6-a698-07e2de0f1f5d}'/><EventID>12</EventID><TimeCreated SystemTime='2026-09-30T19:29:31.9492525Z'/><Execution ProcessID='4' ThreadID='8'/><Channel>System</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='StartTime'>2026-09-30T19:29:31.5000000Z</Data></EventData></Event>`
	display     = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Display'/><EventID>4101</EventID><TimeCreated SystemTime='2026-10-01T12:00:00Z'/><Channel>System</Channel></System><EventData><Data>amdkmdag</Data></EventData></Event>`

	// A record shaped like WerKernel's, written by a user: no provider
	// GUID, a user SID, a user process.
	forgedWer = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T18:08:41.1322011Z'/><Execution ProcessID='9876'/><Channel>Microsoft-Windows-WerKernel/Operational</Channel><Security UserID='S-1-5-21-1-2-3-1001'/></System><EventData><Data Name='ComponentName'>WATCHDOG</Data></EventData></Event>`
	// The same in the Application log, where users may write.
	forgedApp = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel' Guid='{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T18:08:41.1322011Z'/><Execution ProcessID='4'/><Channel>Application</Channel><Security UserID='S-1-5-18'/></System><EventData><Data Name='ComponentName'>WATCHDOG</Data></EventData></Event>`
	// The right GUID and channel with a user SID.
	forgedSID = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-WerKernel' Guid='{87a623f0-8db5-5c11-7c80-a2ebbcbe5189}'/><EventID>1001</EventID><TimeCreated SystemTime='2026-10-01T18:08:41.1322011Z'/><Execution ProcessID='4'/><Channel>Microsoft-Windows-WerKernel/Operational</Channel><Security UserID='S-1-5-21-1-2-3-1001'/></System><EventData><Data Name='ComponentName'>WATCHDOG</Data></EventData></Event>`
	// A standby entry with no provider GUID.
	forgedStandby = `<Event xmlns='http://schemas.microsoft.com/win/2004/08/events/event'><System><Provider Name='Microsoft-Windows-Kernel-Power'/><EventID>506</EventID><TimeCreated SystemTime='2026-10-01T17:25:00Z'/><Channel>System</Channel><Security UserID='S-1-5-21-1-2-3-1001'/></System></Event>`
)

func TestFromXMLReadsTheDaysRecords(t *testing.T) {
	for _, c := range []struct {
		xml    string
		kind   EventKind
		source string
	}{
		{werWatchdog, Reset, "WerKernel 1001 WATCHDOG"},
		{werAMD, Reset, "WerKernel 1001 AMD_WATCHDOG"},
		{liveKernel, ResetCorroboration, "LiveKernelEvent 141"},
		{standbyIn, StandbyEnter, "Kernel-Power 506"},
		{standbyOut, StandbyExit, "Kernel-Power 507"},
		{bootRecordX, bootRecord, "Kernel-General 12"},
		{display, ResetCorroboration, "Display 4101"},
	} {
		ev, ok := FromXML([]byte(c.xml))
		if !ok || ev.Kind != c.kind || ev.Source != c.source || ev.At.IsZero() {
			t.Fatalf("%s read %+v %v", c.source, ev, ok)
		}
	}
	want := time.Date(2026, 10, 1, 18, 8, 41, 132201100, time.UTC)
	if ev, _ := FromXML([]byte(werWatchdog)); !ev.At.Equal(want) {
		t.Fatalf("time %v", ev.At)
	}
	for _, other := range []string{werOther, liveVendor, `<Event/>`, `not xml`} {
		if ev, ok := FromXML([]byte(other)); ok {
			t.Fatalf("%q read as %+v", other, ev)
		}
	}
}

// RES-G1: a record the kernel did not write triggers no reset: no provider
// GUID, a user SID, a user process, or a user-writable channel.
func TestARecordTheKernelDidNotWriteIsNoReset(t *testing.T) {
	for name, forged := range map[string]string{"wer": forgedWer, "app": forgedApp, "sid": forgedSID, "standby": forgedStandby} {
		if ev, ok := FromXML([]byte(forged)); ok {
			t.Fatalf("%s: a forged-style record read as %+v", name, ev)
		}
	}
	r := newRig(t, Options{})
	e, err := r.load("fastwan")
	if err != nil {
		t.Fatal(err)
	}
	ev, ok := FromXML([]byte(liveKernel))
	if !ok {
		t.Fatal("the LiveKernelEvent was not read")
	}
	r.source.events <- ev
	// A second send returns only once the first record was handled.
	r.source.events <- Event{Kind: StandbyExit}
	if !e.Alive() || r.leases.liveOn("card:0") != 1 {
		t.Fatal("a corroborating record alone stopped an engine")
	}
	if err := r.guard.AdmitLoad(t.Context()); err != nil {
		t.Fatalf("a corroborating record alone refused a load: %v", err)
	}
}

// RES-G1: at start a 506 older than the last boot is no standby; a 506 after
// it is; an unknown boot sets a 506 aside; a 507 always stands.
func TestStartupStandbyIgnoresAnEntryFromBeforeTheBoot(t *testing.T) {
	boot := time.Date(2026, 9, 30, 19, 29, 31, 0, time.UTC)
	old := Event{Kind: StandbyEnter, At: boot.Add(-time.Hour)}
	if _, ok := startupStandby(old, true, boot); ok {
		t.Fatal("an unpaired 506 from before the boot read as standby")
	}
	fresh := Event{Kind: StandbyEnter, At: boot.Add(time.Hour)}
	if ev, ok := startupStandby(fresh, true, boot); !ok || ev.Kind != StandbyEnter {
		t.Fatal("a 506 after the boot was set aside")
	}
	if _, ok := startupStandby(fresh, true, time.Time{}); ok {
		t.Fatal("a 506 with no known boot read as standby")
	}
	exit := Event{Kind: StandbyExit, At: boot.Add(-time.Hour)}
	if _, ok := startupStandby(exit, true, time.Time{}); !ok {
		t.Fatal("a 507 was set aside")
	}
	if _, ok := startupStandby(Event{}, false, boot); ok {
		t.Fatal("no record read as a standby change")
	}
}

package panel

// User-facing reasons why an input was ignored (shown as toasts to the
// client that sent it).
const (
	hintNotHalted      = "Works only when the CPU is halted — set ENABLE/HALT to HALT."
	hintMachineOff     = "The machine is off — turn the key to POWER."
	hintStarting       = "The machine is starting — wait until it is up."
	hintStopping       = "The machine is powering off."
	hintMachineBusy    = "The machine is busy — try again."
	hintLocked         = "The key is in LOCK: the console switches are disabled — turn the key to POWER."
	hintLatchLocked    = "The key is in LOCK: ENABLE/HALT and S INST/S BUS CYCLE have no effect until you turn the key to POWER."
	hintSBus           = "S BUS CYCLE is selected: LOAD ADRS, EXAM and DEP do nothing — set S INST."
	hintAdrsErr        = "ADRS ERR is lit: load a valid address with LOAD ADRS first."
	hintBootLocked     = "The console is locked — turn the key to POWER (unlock the console) to boot."
	hintBootHalt       = "Set ENABLE/HALT to ENABLE to boot."
	hintBootStarting   = "The system is already starting."
	hintShutdownIdle   = "The OS is not running, so there is nothing to shut down."
	hintNoScript       = "This system has no automatic shutdown — see the system's help for how to shut it down."
	hintShutdownActive = "A shutdown is already in progress."
	hintQueueFull      = "The panel is busy — input ignored."
)

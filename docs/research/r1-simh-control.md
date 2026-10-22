# R1: Controlling and observing the Open SIMH PDP-11 (11/70) from an external program

Researcher: r1-simh. Date: 2026-10-21.
Host: Ubuntu 26.04 x86_64. It is a QEMU VM with 16 cores, and other researchers' simulators were running at the same time, so all speed numbers carry roughly ±15% noise.

Legend:
* **[V]** means VERIFIED: I ran it, or I read the code at the cited `file:line`.
* **[U]** means UNVERIFIED: memory or inference.

All line numbers refer to the unpatched clone, commit `87eb7d5e`.

`$SCRATCH` stands for the research session's scratch directory, an ephemeral working directory under `/tmp` that no longer exists.

Working directory used below: `R=$SCRATCH/r1-simh`. It contains:
* `$R/simh`: the stock clone and its binary, `$R/simh/BIN/pdp11`
* `$R/simh-panel`: the patched prototype and its binary, `$R/simh-panel/BIN/pdp11`
* `$R/exp`: the harness, the experiments, the raw transcripts (`*.transcript.txt`) and `panel-prototype.diff`

---

## 0. Executive summary

1. **Do not build the appliance on SimH's remote-console master mode or on the frontpanel API as they ship today.** At this commit:
   * **Spurious stop.** The first CONTINUE/GO after entering master mode stops after a few instructions with `Simulation stopped`. This is a timer bug, root-caused and fixed by a one-line patch. [V]
   * **Simulator orphaned.** If the master TCP session drops, the simulator neither exits nor accepts a new master: it answers `All connections busy`. A bridge restart therefore orphans the simulator. [V]
   * **Latency.** Every lock-step command costs about 50 ms. [V]
   * **Missing commands.** RESET, LOAD, DO, RESTORE and NEXT are not allowed. [V]
   * **No DEPOSIT while running.** DEPOSIT is only possible by halting first. [V]
2. **Do not rely on the non-master remote console for writes.** After `^E` (multi-command mode), the first command executes. The simulator then silently resumes with its timer services stopped, and the session goes deaf. [V]

   Single-command mode does work: EXAMINE, SHOW, REPEAT, COLLECT and SAMPLEOUT while running, with about 50 ms latency. REPEAT reaches a clean 60 Hz. [V]
3. **Unpatched sampling cannot drive realistic lamps.** COLLECT/SAMPLEOUT gives per-bit duty cycles, but only of *registers* (PC, PSW, DR, R0 and so on), never of bus address or data. It has further problems:
   * Its window is counted in instructions, so it is about 0.25 ms at the measured 150-250 M instr/s.
   * Its DITHER value is parsed but never stored, so the samples alias. [V]
   * Its sampling unit is not idle-capable, so it defeats `SET CPU IDLE`: 100% host CPU instead of 6%. [V]
4. **Recommended: option (iii), hybrid.**
   * **Patch.** A small SimH patch, prototyped and measured here, of about 50 changed lines in existing files plus a new ~240-line `pdp11_panel.c`. It exports bus-cycle samples, registers, run/wait/halt state, a live switch register and a halt request through an mmap'ed file. It also includes the one-line timer fix.
   * **Control.** All SCP control goes over the simulator's stdin/stdout pipes, owned by the bridge. This is fast (0.05-0.1 ms per command) and gives the full command set.
   * **Console.** The console goes to a localhost raw TCP port: `SET CONSOLE -U TELNET=127.0.0.1:p;NOTELNET` plus `BUFFERED`.

   Measured results with the prototype: [V]
   * Cost: 6-8% at 110-170 MIPS.
   * Live SR changes reach the guest in about 1 ms without halting.
   * HALT via shared memory reaches the `sim>` prompt in 0.1 ms, against 250 ms for SIGINT.
   * Panel frames at 60 Hz carry about 500-800 bus samples each.
   * Idle still works: 6% host CPU in a WAIT loop.

---

## 1. Build [V]

* **Clone.** `git clone --depth 1 https://github.com/open-simh/simh`
  * Commit `87eb7d5e96f9ce0ee6ac183e20160e5c486b0712`, commit date 2026-09-22 12:24:39 -0400 (author date 2026-07-15), "S3: Fix zoned decimal negative number representation".
* **Build command.** `make -j16 TESTS=0 pdp11`
  * Wall time 19.3 s (user 17.8 s). The output is `$R/simh/BIN/pdp11`, 2.6 MB.
  * The build uses `-Werror`, and there were no compiler warnings.
* **Optional-library notices from the makefile:**
  * SDL2/libpng missing ("could provide more functionality if video support was available").
  * `pdp11 Simulator is being built WITHOUT libpcap networking support`. The TAP and NAT(SLiRP) transports are still built in.
  * VDE missing.
  * `PCRE2 not detected`. `SHOW VERSION` then says "No RegEx support for EXPECT commands".
  * Makefile switches that control these: `NOVIDEO=1`, `NONETWORK=1`, `TESTS=0`, `WARNINGS=ALLOWED`, `LTO=1`, `OPTIMIZE=-O3` (makefile:40-73).
* **`SHOW VERSION`** includes "Asynchronous I/O support (Lock free asynchronous event queue)", "Asynchronous Clock support", "FrontPanel API Version 12" and "OS clock resolution: 1ms".
* **Front-panel test program.** `make TESTS=0 frontpaneltest` builds `BIN/frontpaneltest` in 1 s.
* **Patched tree.** `$R/simh-panel` builds cleanly under `-Werror` in 18.7 s. Its diff is `$R/exp/panel-prototype.diff`.

---

## 2. Source analysis

### 2a. Remote console (`sim_console.c`)

**SET REMOTE options.** The table is at sim_console.c:358-367.

| Option | Behaviour |
|---|---|
| `CONNECTIONS=n` | At most 40 (`MAX_REMOTE_SESSIONS`, :468). It must be set before `TELNET=`; otherwise it fails with SCPE_ALATT (:1974-1975). Each line gets a repeat unit and a sample unit (:1992-2012). |
| `TELNET=port[;opts]` | Implemented at :1916-1943. It defaults CONNECTIONS to 1 and sets `buffered=8192`. The port may be `host:port`: `127.0.0.1:42202` was verified to bind to localhost only. Per-port options after `;` are NOTELNET, TELNET, NOMESSAGE, MESSAGE, ACCEPT=rule and REJECT=rule (sim_tmxr.c:3074-3162). |
| `-U` switch | Gives the listener SO_REUSEADDR: `sim_master_sock()` sets it when `sim_switches & SWMASK('U')` (sim_sock.h:125). **Without `-U`, restarting the simulator within the ~60 s TIME_WAIT period fails** with `Sockets: bind error 98 - Address already in use` / `Can't open network port`. [V] The same switch works for `SET CONSOLE -U TELNET=...` and `ATTACH -U DZ ...`. [V] |
| `NOTELNET` | Closes the remote console (:1944-1958). |
| `BUFFERSIZE=n` | Minimum 1400 (:2033-2048). |
| `TIMEOUT=s` | 0-3600, default 30 (:555, :2016-2031). After this many seconds of no input in multi-command mode, a non-master session automatically CONTINUEs (:1556-1571). |
| `MASTER` / `NOMASTER` | Implemented at :2065-2132 and explained below. |

**Two modes of operation.**

* **Non-master ("multiplexed").**
  * There are up to CONNECTIONS sessions, and each starts in single-command mode (:702).
  * Banner on connect [V]: `\xff\xfb"\xff\xfb\x03\xff\xfb\x01\xff\xfb\x00\xff\xfd\x00\n\r\nConnected to the PDP-11 simulator REM-CON device, line 0\r\n\nPDP-11 Remote Console\r\nEnter single commands or to enter multiple command mode enter the ^E character\r\nSimulator Running...`
  * **Single-mode commands** (:826-845): ATTACH, DETACH, EXAMINE, EVALUATE, REPEAT, COLLECT, SAMPLEOUT, EXECUTE, PWD, DIR, LS, ECHO, ECHOF, SHOW, DEBUG, NODEBUG, HELP. There is **no DEPOSIT and no SET**: both give `Invalid remote console command`. [V]
  * **Entering multi-command mode.** `^E` (WRU, `sim_int_char=005`, :191) switches to multi-command mode (:1464-1488), which prints `\r\nSimulation stopped, PC: 001062 (SOB R3,1062)\r\n\r\nSimulator paused.\r\nsim> `. [V]
  * **Multi-mode commands** (:763-785): EXAMINE, DEPOSIT, EVALUATE, ATTACH, DETACH, ASSIGN, DEASSIGN, CONTINUE, REPEAT, COLLECT, SAMPLEOUT, PWD, SAVE, DIR, LS, ECHO, ECHOF, SET, SHOW, HELP. This mode is **broken for the PDP-11** (bug B2 in §4).
  * **Command echo.** Every remote command is also echoed on SimH stdout as `Remote Console Command from 127.0.0.1> EXAMINE PC` together with its output (:1657-1658). With a 60 Hz REPEAT this floods stdout. [V]
  * **Serviced only while running.** Sessions are polled by event units (:689-724, :1382ff). While SCP sits at the stdin `sim>` prompt, a connection receives 0 bytes. [V]
* **Master (`SET REMOTE MASTER`, from a command file).**
  * `sim_set_rem_master` calls `sim_run_boot_prep(0)`: queue flush, `sim_time=0`, `reset_all(0)` (:2084). It then loops `run_cmd(RU_GO,"")` (:2087-2114), so the simulator is permanently "inside" sim_instr. When the master is in multi-command mode, the data service blocks inside an event with `sim_is_running=FALSE` and timers stopped (:1439-1453; the 50 ms poll sleep is at :1572).
  * Line 0 is the master. Its banner ends `...\r\r\nMaster Mode Session\r\nsim> ` [V].
  * **Master commands** (:787-824): EXAMINE, DEPOSIT, EVALUATE, ATTACH, DETACH, ASSIGN, DEASSIGN, CONTINUE, STEP, REPEAT, COLLECT, SAMPLEOUT, EXECUTE, PWD, SAVE, CD, DIR, LS, ECHO, ECHOF, SET, SHOW, HELP, EXIT, QUIT, RUN, GO, BOOT, BREAK, NOBREAK, EXPECT, NOEXPECT, DEBUG, NODEBUG, SEND.
  * **Not allowed** [V] (each answers `Invalid remote console command`): `RESET`, `LOAD`, `DO`, RESTORE, `NEXT`. NEXT's reply is oddly formatted: `Invalid remote console command, PC: 001062 (SOB R3,1062)`.
  * **Console requirement.** The console must be Telnet or serial, otherwise you get "Console port must be Telnet or Serial with Master Remote Console" and the simulator exits (:2813-2824).
  * **Waiting for the master.** SimH waits indefinitely for the master connection, printing "Waiting for Remote Console connection" every 10 s (:2800-2812, retried by the master loop).
  * **While running**, the master session is in single mode and uses the single-mode table. So again there is no DEPOSIT while running (:1710).
* **How commands are executed.**
  * The PDP-11 never calls `sim_set_stable_registers_state()`: grep finds only its definition and declaration (sim_console.c:286, sim_console.h:118). So `sim_con_stable_registers` stays FALSE (:282).
  * Consequently almost every remote command returns `SCPE_REMOTE` (:1790). sim_instr exits, `run_cmd` calls `sim_remote_process_command()` (scp.c:9365-9371), and then sim_instr re-enters. On re-entry, sim_instr rebuilds `dib_tab` and restores state (pdp11_cpu.c:743-763).
  * Command output is captured through a temporary log file `sim_remote_console_<pid>.temporary_log` in the **current directory** (:1685-1694) and replayed to the socket (:896-920). The working directory must therefore be writable.
  * A quirk of this replay: the previous command's output is sometimes sent again in front of the next message [V], for example `PC:\t001062\r\n...\r\nSimulation stopped...`.
* **Latency.** The data service is re-armed every 100 ms (:1882), and the multi-mode wait loop sleeps 50 ms (:1572).
  * Lock-step command round trip: about 50 ms in both modes. Repeated single-mode `EXAMINE PC` gave 49-108 ms, and still about 50 ms with a 60 Hz REPEAT active. [V]
  * Pipelining helps a lot: 59 `DEPOSIT`s sent in one write completed in 90 ms, and `EXECUTE a;b;...` did 59 deposits in 54 ms. The same 59 deposits took 2980 ms lock-step. [V]
* **CONTINUE, STEP, RUN, GO and BOOT in master mode.**
  * `RUN`, `GO` and `BOOT` run `run_cmd` with `SIM_SW_HIDE`, which does setup only and returns early (scp.c:9318-9319). The command is then turned into CONTINUE (:1737-1748).
  * RUN and BOOT reset all devices and `sim_time`; RUN prints "Resetting all devices... This may not have been your intention." [V]
  * CONTINUE prints `Simulator Running...` (:1828).
  * `STEP n` uses `sim_step` (:1869-1876) and finishes with `\r\n\r\nStep expired, PC: 001062 (SOB R3,1062)\r\n...sim> `. In a WAIT state, STEP does not advance the PC, because each wait-loop iteration counts as one step. [V]
  * BOOT in master mode works [V]: `ATTACH -E RL0 rl0.dsk` then `BOOT RL0` gives `Simulator Running...\r\nHALT instruction, PC: 000002 (HALT)\r\nsim> `. The image was all zeros.
* **Halting from a remote client.** Send byte 0x05 while running [V]. The reply is `\r\nSimulation stopped, PC: 001044 (MOV @#177570,R5)\r\nsim> `. Sending 0x05 while already halted adds it to the next command line, which then fails with `Unknown command`. [V] The bridge must track state.
* **Prompts.**
  * Master, halted: `sim> ` (:1521).
  * While running, the first typed character of a command makes SimH emit `\r\nSIM> ` in master mode (:1505) or `\r\nsim> ` in non-master mode.
  * Commands issued by REPEAT or EXECUTE are echoed as `SIM> CMD` in master mode and `sim> CMD` in non-master mode (:1529-1546).
  * There is no trailing prompt after a single-mode command's output. Delimit responses with `ECHO <marker>`.

**REPEAT, COLLECT and SAMPLEOUT.** These are remote-only commands (:847-853).

* **REPEAT.**
  * Syntax: `REPEAT EVERY <n> USECS <cmd>[;<cmd>...]`, `REPEAT STOP`, `REPEAT STOP ALL` (the last on line 0 only) (:1022-1100).
  * It is scheduled on wall-clock time (`sim_activate_after`, :1087, :1305) and fires only while the CPU runs. It produced 0 samples while halted in master mode. [V]
  * Measured [V]: `REPEAT EVERY 16667 USECS EXAMINE PC;EXAMINE SYSTEM DR;SHOW TIME` gave 57.5-60.5 Hz. Output per tick: `sim> EXAMINE PC\r\nPC:\t001062\r\nsim> EXAMINE SYSTEM DR\r\nDR:\t004000\r\nsim> SHOW TIME\r\nTime:\t612880911\r\n`.
* **COLLECT.**
  * Syntax: `COLLECT <depth> SAMPLES EVERY <n> CYCLES DITHER <pct> PERCENT [-I ][<dev> ]<reg>[,[-I ][<dev> ]<reg>...]`, `COLLECT STOP`, `COLLECT STOP ALL` (:1103-1295).
  * **The DITHER clause is mandatory**: without it you get `%SIM-ERROR: Expected DITHER found: SYSTEM` [V]. Its value is range-checked (0-25) but never stored (bug B3).
  * Samples come from a unit scheduled in **instructions** (`sim_activate`, :1291, :1375). Each bit keeps a rolling window of `depth` samples (:1311-1345). `-I` means indirect: the register value is used as a memory address (:1335-1336).
  * Always qualify registers with their device (`SYSTEM DR`, `CPU PC`). An unqualified name is looked up in whatever `sim_dfdev` the last command left behind (:1229).
* **SAMPLEOUT output format** (:567-588).
  * One line per register: `}<DEV> <REG>[ -I] <depth>:<b0>,<b1>,...` (arrays: `}<DEV> <REG>[i] <depth>:`).
  * Values are listed **LSB first**. Each value is the number of the last `depth` samples in which that bit was 1.
  * While halted, the windows are filled with 0 or depth for the current value (:1339-1343).
  * Real output [V], 50 samples every 10000 instructions: `}SYSTEM DR 50:9,1,0,8,2,0,4,6,0,1,9,0,0,8,2,0` and `}CPU PC 50:0,50,1,0,50,50,0,0,0,50,0,0,0,0,0,0` (PC=001062).
  * `SHOW REMOTE` describes the sampling setup, and `SHOW -D REMOTE` also prints the sample lines (:680-681).

### 2b. Frontpanel API (`sim_frontpanel.c/.h`, API version 12)

* **How it launches the simulator** (`_sim_panel_create`, sim_frontpanel.c:687-998).
  * **Port choice.** It picks a TCP port by *connecting* to 1024..2047 and taking the first one that refuses (:741-758). This is racy and uses low ports.
  * **Temporary config.** It copies your config to `<config>-Panel-<pid>` and appends `set remote notelnet`, optionally `set remote connections=N`, then `set remote -u telnet=<port>`, `set remote master` and `exit` (:789-809).
  * **Process start.** It runs `fork()` plus `execlp(sim, tempconfig)` with **stdin, stdout and stderr all on /dev/null** (:867-875). Your config must therefore put the guest console on Telnet or serial.
  * **Handshake.** It connects with up to 100 retries 100 ms apart (:884-890), sends the telnet mantra (:909), checks `SHOW VERSION` for "FrontPanel API Version 12" and reads `SHOW <dev> RADIX` (:947-973).
* **Wire protocol** (constant strings at :213-231). It is plain text over the master remote console.
  * Every command ends with `\r`. A command whose reply is needed is followed by `ECHO Status:%STATUS%-%TSTATUS%\r# COMMAND-DONE\r` (:2660-2664).
  * **Register fetch:** `EXECUTE # REGISTERS-START;show time;# REGISTERS-FOR-DEVICE:<dev>;E -16 <dev> R1,R2,..;# REGISTER-INDIRECT:<r>;E -16 <dev> <r>,$;sampleout;# REGISTERS-DONE` (:482-587).
  * **While running:** `repeat every <us> usecs # REGISTERS-REPEAT-START;...;# REGISTERS-REPEAT-DONE` (:2486-2512).
  * **Bit sampling:** `collect <depth> samples every <freq> cycles dither <pct> percent [-I ][DEV ]REG,...` (:623-626). The default dither is 5% (:1448-1451), which has no effect (bug B3).
  * **Halt:** send `\005` and wait for `sim> ` (:1466).
  * **Run, start, boot and step:** `CONT`, `RUN` and `BOOT dev` wait for "Simulator Running..."; `STEP` waits for the prompt (:1515, :1553, :1575, :1595).
  * **Reader thread.** It parses `NAME:hex` lines and `}DEV REG depth:` lines (:2189-2309). Run/Halt state is inferred from the text "Simulator Running..." and from the prompt (:2385-2419).
  * **No access while running.** Every examine, deposit or add-register call returns "Not Halted" while the simulator runs (e.g. :1155, :1723, :1842, :1941).
* **Test.** I compiled `$R/exp/fp_pdp11.c` with `gcc -O2 -I../simh fp_pdp11.c ../simh/sim_frontpanel.c ../simh/sim_sock.c -lpthread` and ran it against `BIN/pdp11` (11/70, 4M, buffered telnet console). **It works**, with these measurements [V] (transcript `exp/fp_pdp11.transcript.txt`):
  * Start: 2262 ms.
  * 59 `sim_panel_mem_deposit` calls: 2977 ms, about 50 ms each.
  * `sim_panel_exec_run` right after start: **spurious halt** (bug B1); the halt text was `Simulation stopped, PC: 001062 (SOB R3,1062)`.
  * `sim_panel_exec_start` (RUN): works.
  * Callback requested at 20000 µs (50 Hz): **33.5 Hz actually delivered**. SimH time advanced 71 M instructions per second (the busy loop with REPEAT and COLLECT active).
  * `DRbits[]`: populated, values 0-64.
  * `gen_deposit("SR")` while running: `Not Halted`.
  * halt + deposit + run: 104 ms.
  * halt: 15 ms.
  * step: 41-142 ms each.
  * destroy: 433 ms.
  * The WAIT experiment showed DR frozen. The cause: `exec_start` = RUN resets the KW11-L, and the guest's init code did not run again. This is SimH RUN semantics, not an API bug.

### 2c. Console routing

* **The default console** is the process's own stdin/stdout, shared with the SCP prompt.
  * When stdin is not a tty (a pipe), keyboard input is not polled at all: `if (sim_ttisatty ()) c = sim_os_poll_kbd (); else c = SCPE_OK;` (:2907-2910).
  * A bridge that owns SimH through pipes must therefore move the console to TCP.
* **`SET CONSOLE TELNET=port`** (sim_set_telnet, :2489-2520). The modifiers LOG, NOLOG, BUFFERED[=n], NOBUFFERED and UNBUFFERED are handled first; anything else goes to `tmxr_attach`.
  * **SCP output never goes to the console session.** The SCP prompt, the "Simulation stopped" messages and all command output stay on stdout (or on the remote session). [V]: the console stream carried only guest bytes across halts.
  * **Bytes sent on connect (telnet mode)** [V]: `\xff\xfb"` IAC WILL LINEMODE(34), `\xff\xfb\x03` WILL SGA, `\xff\xfb\x01` WILL ECHO, `\xff\xfb\x00` WILL BINARY, `\xff\xfd\x00` DO BINARY. This "mantra" is defined at sim_tmxr.c:473-479. It is followed by `\n\r\nConnected to the PDP-11 simulator CON-TELNET device\r\n\n` (sim_tmxr.c:553-576).
  * **`;NOTELNET`** (raw mode) [V]: zero bytes on connect, with no IAC and no banner. Data is a pure byte stream; `NOMESSAGE` suppresses the banner on its own. **Recommended**, because the bridge then needs no IAC parsing.
  * **UNBUFFERED** (the default). If the console is not connected, `sim_check_console(30)` waits up to 30 s printing "Waiting for console Telnet connection" (:2847-2869). A dropped connection makes `sim_putchar` return SCPE_LOST (:2954-2956).
  * **BUFFERED[=n]** [V]. The CPU runs without a client ("%SIM-INFO: Running with Buffered Console", :2835-2846). A new connection gets the banner plus the last n bytes; the reconnect replay was verified.
  * **Connection polling** happens once per second (:294-308). A reconnect can therefore take up to 1 s to be accepted. [V]
  * **WRU on the console session does not halt.** A `^E` arriving on the console TCP session (telnet or notelnet) is delivered to the guest as data: after a SIGINT, RBUF (17777562) read 000005 and RCSR read 000200. [V] Halting is out of band.
* **Master mode plus console.** Master mode requires a Telnet or serial console (:2813-2824). In master mode `sim_poll_kbd` never reads stdin (:2903). The console session and the master session are independent TCP connections.
* **SET CONSOLE SERIAL=dev[;cfg]** (:2638ff) attaches the console line to a host serial port. Code reading only [U]; it was not tested.
* **Character modes.** TTO defaults to `TT_MODE_7P` (pdp11_stddev.c:187). 7P drops DEL and any control character not in PCHAR; PCHAR is set to `01000023640` = ESC, CR, LF, HT, BS, BEL and ENQ (pdp11_cpu.c:3444, sim_console.c:3040-3043). Use `SET TTO 7B` or `8B` for a faithful terminal stream. TTI defaults to 7P (pdp11_stddev.c:121), which only strips bit 7 on input.
* **DZ11 (`dz`)** [V].
  * `SET DZ ENABLED`, `SET DZ LINES=8`, then `ATTACH -U DZ 127.0.0.1:42203;notelnet` (raw) or `ATTACH -U DZ 41602` (telnet). Individual lines can be placed on separate ports with `line=n,port`.
  * Connections are serviced **only after the guest sets CSR MSE** (pdp11_dz.c:568-570). With MSE set, a telnet connection receives the mantra plus `\n\r\nConnected to the PDP-11 simulator DZ device, line 0\r\n\n`. Before that, it receives nothing. [V]
  * The default after `SET DZ ENABLED` is 16 lines at 17760100, vector 300. [V]
* **Multi-line DL11 (`dli`/`dlo`).** `SET DLI ENABLED`, `SET DLI LINES=2`, `ATTACH -U DLI line=0,41603;notelnet,line=1,41604` [V]. It accepts connections (`SHOW DLI` reported "2 current connections"), but neither line sent bytes within 1.5 s in my test. The cause is UNVERIFIED; it is probably the modem/dataset handling (pdp11_dl.c:234-235, 383-413).

### 2d. PDP-11 CPU internals

* **Switch register (SR) and display register (DR).**
  * Both are globals in `pdp11_cpumod.c:75-76` (`int32 SR, DR`). They are exposed as registers of the **SYSTEM** device, not CPU (pdp11_cpumod.c:279-289): `EXAMINE SYSTEM SR`.
  * `EXAMINE SR` also works through the global register lookup (scp.c:11001-11039), as long as no other enabled device has an SR register. KG11 has one and is disabled by default.
  * Bus decoding: `sr_dib = {IOBA_SR, IOLN_SR, &SR_rd, NULL}` and `dr_dib = {IOBA_SR, IOLN_SR, NULL, &DR_wr}` (pdp11_cpumod.c:177-178), both registered through `cnf_tab` for HAS_SR and HAS_DR (:253-254). `IOBA_SR = IOPAGEBASE+017570` (pdp11_defs.h:565). HAS_SR and HAS_DR include the 11/70 (pdp11_defs.h:219-222).
  * `SR_rd` is `*data = SR` (pdp11_cpumod.c:361-365). `DR_wr` is `DR = data` (:367-371); it ignores the byte-write merge, so a MOVB to 177570 replaces DR.
  * So **the console `DEPOSIT 17777570 x` writes DR, and `EXAMINE 17777570` reads SR.** [V] Only the low 16 bits exist, while the real 11/70 has 22 switches; the bridge must keep the 22-bit LOAD ADRS value itself.
* **`cpu_reg[]` names** (pdp11_cpu.c:449-614):
  * PC (`saved_PC`), R0-R5, SP (current mode/set, re-pointed by `set_r_display`), R00-R05 and R10-R15 (both register sets), KSP, SSP and USP.
  * PSW (with bit fields), CM, PM, RS, FPD, IPL, T, N, Z, V and C (sub-fields of PSW).
  * PIRQ, STKLIM, FAC0H-FAC5L, FPS, FEA, FEC, MMR0-MMR3, and KIPAR0..UDPDR7.
  * IREQ[0-7], TRAPS, **WAIT** (`wait_state`, :603), STOP_TRAPS (default 1 = stop on red stack trap), STOP_VECA, STOP_SPA, PCQ[0-63] (PC history), WRU, and the hidden MODEL and OPTIONS.
  * The SYSTEM device (pdp11_cpumod.c:279-336) holds SR, DR, MEMERR, CCR, MAINT, HITMISS, CPUERR, MBRK, WCS, SYSID (default 0x1234), JCSR and so on.
* **Mode and PSW.** The working copies are `cm, pm, rs, ipl, tbit, N, Z, V, C` (pdp11_cpu.c:288-294), assembled by `get_PSW()` and split by `put_PSW()`. The register-visible PSW, REGFILE, STACKFILE and saved_PC are only synchronised at event boundaries (:850-876) and at exit (:2441-2447). `EXAMINE` while running sees a state that is consistent at instruction granularity, because SCPE_REMOTE commands run outside sim_instr.
* **Physical address size.** In SimH, `SET CPU 4M` gives 4186112 bytes (17760000 octal = 4 MB minus the I/O page), reported as `4088KB`.
* **WAIT and idle detection.**
  * `WAIT` sets `wait_state = 1` (:1031-1033).
  * The loop then does `if (wait_state) { sim_idle (TMR_CLK, TRUE); continue; }` (:955-958), so the PC stays at the WAIT instruction + 2. Any trap or interrupt clears wait_state (:915).
  * `sim_idle` (sim_timer.c:1545ff) sleeps only if all of these hold: idling is enabled, the queue head is idle-capable (:1566-1571), and the clock has been stable for `sim_idle_stable` seconds. That is 20 s by default (sim_timer.h:85) and can be changed with `SET CPU IDLE=2` (sim_timer.c:1656-1673). The sleep advances `sim_interval` by the cycles that would have run.
  * **`SET CPU IDLE` and `SET THROTTLE` cancel each other** (sim_timer.c:1668-1671, 1735-1738). [V] Messages "Throttling disabled" / "Idling disabled".
* **Where translation happens** (function names and the hot path):
  * **Instruction fetch:** `ReadE(PC | isenable)` (:979, body :2629-2654). It relocates through `relocR`, then reads memory directly or the I/O page through `iopageR`.
  * **Data reads:** `ReadW` and `ReadB` (:2656, :2672) call `relocR` (:2867), then `PReadW` and `PReadB` (:2727, :2744). Read-modify-write uses `ReadMW` and `ReadMB` (:2703, :2717), which call `relocW` (:2976) and keep the result in `last_pa` (:330). The write-back is `PWriteW(dst, last_pa)`.
  * **Writes:** `WriteW` and `WriteB` (:2770ff) call `relocW`, then `PWriteW` and `PWriteB` (:2817, :2834). Trap and interrupt vector fetches and pushes use `ReadCW` and `WriteCW`.
  * **The physical funnel** is therefore ReadE, PReadW, PReadB, PWriteW and PWriteB. The FPP (pdp11_fp.c) calls `PWriteW` directly. Device DMA (Map_ReadW and so on, pdp11_io_lib.c) bypasses this funnel.
  * **Console access:** `cpu_ex` and `cpu_dep` (:3528, :3555) take physical addresses by default. `-V` selects virtual through `relocC` (:3065-3100), with `-K`, `-S` or `-U` for the mode (default the current mode, `-P` the previous mode) and **`-T` for D-space**. I/O-page reads use `READC`.
  * `relocR` and `relocW` are ordinary functions in the same file, which gcc's `-finline-functions` may inline [U].
* **sim_instr() structure** (:726-2448).
  1. Restore state (:743-763) and `setjmp` for aborts (:782).
  2. `while (reason == 0)` (:837):
     * `cpu_astop` check (:843-847).
     * `AIO_CHECK_EVENT`.
     * **Event processing when `sim_interval <= 0`** (:850-876): save registers, `sim_process_event()`, restore. This is fairly expensive; it includes `set_r_display`, which does a `find_reg("R0")` linear search.
     * Trap and interrupt dispatch (:878-949).
     * tbit and the WAIT check (:953-958).
     * The breakpoint check (:968-973) and the MMR1/MMR2 update (:975-978).
     * **Fetch, `IR = ReadE(..)`; `sim_interval--`** (:979-980).
     * History (:985-1007).
     * Decode switch (:1009-2437).
  3. Exit (:2439-2448).

  **The cheapest place for an every-N-instructions hook is immediately before the fetch at :979.** The prototype puts a `--countdown` there. The existing event mechanism costs an exit and restore per event.
* **HALT and console halt.**
  * The `HALT` instruction (:1021-1030) in kernel mode sets `reason = STOP_HALT`. Printed message: `HALT instruction, PC: 004002 (HALT)` [V]. The PC shown is after the HALT. In other modes HALT gives a privileged or illegal-instruction trap.
  * A console halt works by setting `stop_cpu`: from SIGINT, SIGTERM or SIGHUP (int_handler, scp.c:9582-9588) or from a WRU on a tty stdin (sim_console.c:2911-2913). `sim_process_event` then returns SCPE_STOP (scp.c:11932-11935). The message is `Simulation stopped, PC: ...` (scp.c:801, printer scp.c:9489-9531).
  * **Every SCPE_STOP adds a fixed 250 ms sleep** (`sim_stop_sleep_ms`, scp.c:644, 9417-9420). [V] SIGINT to prompt took 250.4 ms.
* **Reset.**
  * The `RESET` instruction (:1040-1053) calls `reset_all(2)`, which skips the CPU and the SYSTEM registers. It then clears PIRQ, STKLIM, MMR0, MMR3, `int_req` and BME.
  * The SCP `RESET` command calls `reset_all(0)`, which includes `cpu_reset` (:3426ff): PIRQ, STKLIM, PSW=0, MMR0-3, trap_req and wait_state. The PC is not changed. On the first call, memory is allocated and autoconfiguration runs.
  * `RUN` resets everything (`sim_run_boot_prep`, scp.c:9462-9480).
* **Start-up side effect** [V]. Clock pre-calibration deposits `INC 120; INC 122; BR 100` at 000100 and sets PC=100 (pdp11_cpu.c:3378-3383, sim_timer.c:3393-3420). That memory is not restored, so a fresh simulator shows PC=000104 and code at vector 100 (the KW11-L vector).

---

## 3. Live experiments

**Harness.** `$R/exp/simhlib.py` uses only the standard library (`subprocess`, `socket`, `threading`). It provides `Sim` (spawn, stdout capture with timestamps, stdin, `/proc` CPU time), `TCP` (raw-byte session with regex waits) and `RemCon` (commands delimited by `ECHO @@DONEn@@`).

**Test program.** `$R/exp/asm11.py` is a two-pass mini-assembler that computes branch and SOB offsets. Its listing was **verified by SimH's `EXAMINE -M 1000-1152`**, which disassembled it identically [V].

```
000100: 002000 000340          clock vector -> 2000, PSW 340
001000: 012706 001000          mov #1000,sp
001004: 012701 000001          mov #1,r1
001010: 012702 000040          mov #40,r2
001014: 012704 000101          mov #101,r4        ; 'A'
001020: 012737 000100 177546   mov #100,@#177546  ; LKS IE
001026: 005037 177776          clr @#177776       ; PSW=0
001032: 010137 177570    loop: mov r1,@#177570    ; DR <- pattern
001036: 000241                 clc
001040: 006101                 rol r1
001042: 005501                 adc r1             ; 16-bit rotate
001044: 013705 177570          mov @#177570,r5    ; R5 <- SR
001050: 100002                 bpl dodly          ; SR<15>=0 -> delay
001052: 000001                 wait
001054: 000403                 br cnt
001056: 012703 001000   dodly: mov #1000,r3
001062: 077301                 sob r3,.
001064: 005302           cnt:  dec r2
001066: 001361                 bne loop
001070: 012702 000040          mov #40,r2
001074: 105737 177564    wtx:  tstb @#177564
001100: 100375                 bpl wtx
001102: 110437 177566          movb r4,@#177566   ; XBUF
001106: 005204                 inc r4
001110: 020427 000133          cmp r4,#133
001114: 001346                 bne loop
001116: 012704 000101          mov #101,r4
001122: 105737 177564 / 100375 / 112737 000015 177566   ; CR
001136: 105737 177564 / 100375 / 112737 000012 177566   ; LF
001152: 000727                 br loop
002000: 005237 003000 000002   isr: inc @#3000 ; rti   (tick counter)
```

Console output is `ABCDEFGHIJKLMNOPQRSTUVWXYZ\r\n` repeated [V]. The DR shows a rotating single bit; REPEAT saw all 16 positions [V].

**Experiments.** Every script has a raw transcript next to it in `$R/exp`.

| # | Script | What it established |
|---|---|---|
| 1 | `exp1_master.py` | Master-mode banner bytes; SHOW, DEPOSIT, EXAMINE (memory, I/O page, registers); RESET/LOAD/DO/NEXT rejected; COLLECT syntax; 50 ms per command; first observation of the stop bug |
| 1b | `exp1b_master_running.py` | `GO 1000` → `Simulation stopped, PC: 001004` after 1 instruction; `SHOW TIME` = -4 |
| 1c | `exp1c_stopbug.py` | Root cause: negative `sim_gtime()`. Workaround `STEP 1000` (time → +994), after which CONTINUE runs; RUN works because it resets time to 0 |
| 1d | `exp1d_fixcheck.py` | Unpatched: `GO 1000` → `Simulation stopped, PC: 001010` with 0 console bytes. **Patched: runs, 14567 console bytes in 1.5 s**, and `^E` then halts cleanly |
| 2 | `exp2_multiplexed.py` | stdin SCP: 0.05-0.1 ms per command; non-master session silent while halted; single-mode EXAMINE about 50 ms; DEPOSIT rejected; REPEAT at 60 Hz; COLLECT/SAMPLEOUT; SIGINT halt 250 ms; SIGINT + DEPOSIT + CONT 300 ms; `HALT instruction, PC: 004002 (HALT)`; `Unit not attached` |
| 2b | `exp2b_multimode.py` | `^E` then EXAMINE works, then the session goes deaf while the guest keeps running (console bytes keep flowing); only SIGINT recovers |
| 3, 4 | `exp3_wait_idle.py`, `exp4_overhead.py` | Speed, idle, throttle and sampling cost (table below) |
| 5, 5b, 5c | `exp5*.py` | Console telnet/notelnet bytes, buffered replay, DZ (MSE gating), DLI; master drop → `All connections busy`, simulator neither exits nor reconnects |
| 6 | `exp6_bench.py` | Patch cost |
| 7, 7b | `exp7_bridge.py`, `exp7b_idle_panel.py` | Hybrid prototype end to end |
| 8 | `exp8_misc.py` | `^E` on the console session goes to the guest; master-mode ATTACH and BOOT |
| 9 | `exp9_pipeline.py` | Pipelined master command throughput |
| FP | `fp_pdp11.c` | Frontpanel API against the pdp11 binary |

**Key raw transcripts** [V], reproduced as captured (excerpts):

```
# master halt via ^E
>>> b'\x05'  ->  b'PC:\t001062\r\nR3:\t000404\r\nR4:\t000105\r\n\r\nSimulation stopped, PC: 001044 (MOV @#177570,R5)\r\nsim> '
              (the first line is the previous command's output being replayed)
# master, while running: command echo and response, no trailing prompt
>>> b'EXAMINE PC,R3,R4\r' -> b'\r\nSIM> EXAMINE PC,R3,R4\r\nPC:\t001062\r\nR3:\t000404\r\nR4:\t000105\r\n'
# master, halted
>>> EXAMINE 17777570 -> '17777570:\t000001'        (reads SR)
>>> DEPOSIT 17777570 123456 ; EXAMINE SYSTEM DR -> 'DR:\t123456'   (writes DR)
>>> RESET -> 'Invalid remote console command'
>>> STEP  -> '\r\n\r\nStep expired, PC: 001062 (SOB R3,1062)\r\n%SIM-INFO: Running with Buffered Console\r\nsim> '
# stdin SCP
'step'      -> b'\nStep expired, PC: 001054 (BR 1064)\nsim> '
'go 4000'   -> b'\nHALT instruction, PC: 004002 (HALT)\nsim> '
SIGINT      -> b'\nSimulation stopped, PC: 001054 (BR 1064)\nsim> '     (250.3 ms)
# frontpanel halt text: '\nSimulation stopped, PC: 001062 (SOB R3,1062)\n%SIM-INFO: Running with Buffered Console\n'
```

**Speed, idle and sampling** [V]. Busy loops at full speed use 100% of one host core.

| Condition | Emulated speed | Host CPU |
|---|---|---|
| Stock binary, SOB tight loop (STEP-timed, 4 runs) | 167-174 MIPS (median 171.6) | 100% |
| Stock binary, memory-copy + JSR/RTS mix | 115-121 MIPS (median 118.7) | 100% |
| Test program busy loop, across runs | 133-245 MIPS | 100% |
| Busy loop + `COLLECT 64 ... EVERY 1000 CYCLES` | within noise | 100% |
| Busy loop + `COLLECT ... EVERY 100 CYCLES` | about -10 to -15% | 100% |
| Busy loop + `REPEAT EVERY 16667 USECS` (2 EXAMINEs) | within noise | 100% |
| WAIT loop, idle off | 400-500 M "instr"/s of sim time (wait iterations count) | 100% |
| **WAIT loop, `SET CPU IDLE=2`, no sampling** | (sim time still advances) | **6-8%** |
| WAIT loop, idle on, REPEAT 60 Hz only | | 11% |
| **WAIT loop, idle on, COLLECT active** | | **100%** (idle defeated) |
| `SET THROTTLE 1M`, busy or WAIT | 0.92-0.96 MIPS | 2-3% |
| `SET THROTTLE 10%` (measured after 4 s, still calibrating) | 0.88 MIPS | 2% |

**What sampling shows during WAIT with idle on** [V]:
* PC is pinned at 001054 (the WAIT instruction + 2), and `EXAMINE WAIT` gives `WAIT:\t1`.
* The KW11-L keeps ticking at 60.0 Hz (the ISR counter at 3000 increments). DR therefore advances once per tick, which REPEAT, and the patched ring, both show.
* A COLLECT window in instructions (60 × 1000 = 60k "instructions", about 0.12 ms at 500 M/s) shows **one constant DR bit**.
* With throttle at 1M, the same COLLECT window spans about 60 ms and shows 4 DR bits lit (15, 16, 15, 14 of 60).

---

## 4. Bugs and quirks found at commit 87eb7d5e (all relevant to a bridge)

| # | Issue | Evidence |
|---|---|---|
| B1 | **Master mode: CONTINUE/GO stops after a few instructions** (`Simulation stopped, PC: 001004`). `sim_start_timer_services` arms the INT-STOP unit when `sim_timer_stop_time (default 0) > sim_gtime()` (sim_timer.c:2574-2575). In master mode, `sim_gtime()` is negative (`SHOW TIME` → -1, -4) because of `sim_activate(rem_con_data_unit, -1)` calls. The frontpanel `sim_panel_exec_run` hits the same bug. | [V] exp1b, 1c and FP. **Fix [V]:** add `(sim_timer_stop_time > 0) &&`. Workaround: `STEP 1000` before CONTINUE, or use RUN (which resets time to 0). |
| B2 | **Non-master multi-command mode (`^E`) is broken for the PDP-11.** The first command runs through SCPE_REMOTE. The session then never re-enters the blocking loop (sim_console.c:1455-1462 does `continue` when no input is pending). The guest resumes with `sim_is_running=FALSE` and timers stopped (:1466-1469), and the session goes deaf. | [V] exp2b. Root cause INFERRED from the code. |
| B3 | **COLLECT DITHER is parsed but never stored** (`dither_pct` :1185 is never assigned to `rem->smp_sample_dither_pct`, :548). Sampling therefore aliases: bits 2, 5, 8, 11 and 14 of the rotating DR were never sampled at 10000-cycle intervals. The formula used if it were set (:1290, :1373) gives ±pct% *of* the interval rather than interval ± pct%. | [V] grep plus SAMPLEOUT output |
| B4 | **COLLECT defeats idle.** Sample units are created with only `UNIT_DIS` (:2006), which is not idle-capable (sim_timer.c:1569-1570). | [V] 100% versus 6% host CPU |
| B5 | Remote command latency of about 50 ms (100 ms poll at :1882, 50 ms sleep at :1572). | [V] |
| B6 | Master session drop: the simulator neither exits ("Master Session Disconnect", :1866-1868, never triggers) nor accepts a reconnect (`All connections busy`). | [V] 30 s while halted, 15 s while running |
| B7 | Without `-U`, listeners cannot be re-bound for about 60 s after a restart. | [V] |
| B8 | A fixed 250 ms sleep after every WRU or SIGINT stop (scp.c:644, 9417-9420). | [V] |
| B9 | `^E` sent while already halted pollutes the next master command; replayed output is duplicated; there is no trailing prompt in single mode. | [V] |
| B10 | Non-master sessions get no service while halted, and every remote command is echoed on stdout. | [V] |
| B11 | Clock pre-calibration leaves code at 000100-000122 and PC=000104. | [V] |
| B12 | DZ connections are serviced only after the guest sets MSE. | [V] |

---

## 5. Recommendation for the bridge

### 5.1 The three options

| Requirement | (i) Unpatched SimH (remote console / frontpanel) | (ii) Patch: everything through shared memory | **(iii) Hybrid (recommended)** |
|---|---|---|---|
| (a) Pure console byte stream | `SET CONSOLE -U TELNET=127.0.0.1:p;NOTELNET`, `BUFFERED`: yes | same | same |
| (b) Halt, cont, step, examine, deposit, start, boot, attach | Master mode: B1, B6, 50 ms per command, no RESET/LOAD/DO, no DEPOSIT while running. Or stdin + SIGINT: works, with 250 ms halt | A command mailbox in shared memory would have to re-implement SCP: much patch, little gain | **SCP over stdin/stdout (0.1 ms, full command set) + halt bit in shm (0.1 ms)** |
| (c) 30-60 Hz panel: bus addr/data, PSW/mode, DR, run/halt/wait, duty cycles | REPEAT at 60 Hz plus COLLECT: registers only (PC as "address"), no bus cycles, instruction-count windows, broken dither, idle defeated, stdout flooded | ring of bus samples, adaptive rate, regs seqlock | same as (ii) |
| (d) Live SR | Halt → deposit → continue: 300 ms via SIGINT, 104 ms via frontpanel (which also needs B1 avoided) | `sr` word read by `SR_rd` | **same as (ii): ~1 ms** |
| Cost | 0 to 5% (sampling) | 6-8% measured | 6-8% measured |
| Robustness | Master mode orphans the simulator on bridge restart (B6) | good | good; a bridge crash still ends SimH through stdin EOF, which a supervisor restarts |

**Verdict.** Use **(iii)**.

* Use (i) only as a no-patch fallback, built on stdin SCP plus SIGINT plus a non-master single-mode `REPEAT EVERY 16667 USECS EXAMINE PC,PSW,R0;EXAMINE SYSTEM DR` for slow, approximate lamps. Never use master mode or the frontpanel API.
* (ii) is unnecessary: SCP over a pipe is already the most complete and fastest command channel.

### 5.2 The patch, as prototyped and measured

The full diff is `$R/exp/panel-prototype.diff` (546 lines including two new files). Changes to existing files:

* **`PDP11/pdp11_cpu.c`** (+35 lines):
  * `panel_run_state(1)` after the state restore in `sim_instr` (after :763), and `panel_run_state(0)` before `return reason` (:2448).
  * `PANEL_INSTR()` (`if (--panel_countdown <= 0) panel_sample()`) immediately before `IR = ReadE(...)` (:979). This samples the last bus cycle of the previous instruction.
  * In the WAIT path (:955-958), `if (--panel_countdown <= 0) panel_wait_poll();`.
  * `PANEL_BUS(pa, data, flags)`, two global stores, in `ReadE` (IFETCH flag, memory and I/O paths), `PReadW`, `PReadB` (BYTE flag), `PWriteW` (WR flag) and `PWriteB` (WR|BYTE flags).
* **`PDP11/pdp11_cpumod.c`, `SR_rd`** (+2 lines): `if (panel_shm && panel_shm->sr_valid) SR = panel_shm->sr & 0177777;`
* **`PDP11/pdp11_defs.h`:** `#define STOP_PANEL (TRAP_V_MAX + 9)`.
* **`PDP11/pdp11_sys.c`:** add `&panel_dev` to `sim_devices[]` and the stop message "Front panel HALT".
* **`makefile`:** add `pdp11_panel.c` to `PDP11`. A production patch also adds it to `UC15` and `PDP11/CMakeLists.txt`.
* **`sim_timer.c:2574`:** `if ((sim_timer_stop_time > 0) && (sim_timer_stop_time > sim_gtime()))`. This is the B1 fix and is worth sending upstream.

**New `PDP11/pdp11_panel.{c,h}`.** This is a `PANEL` device: `ATTACH PANEL <file>` opens, `ftruncate`s and `mmap(MAP_SHARED)`s the file, and keeps the bridge's fields if the magic matches.

`panel_sample()` writes one 8-byte ring entry:
* `addr` = phys<21:0> | WR | BYTE | IFETCH | cm<<25 | WAIT | MME | M22
* `data` (16 bits)
* `pc` (16 bits)

It then publishes `ring_wr` with release semantics, checks `halt_req` (setting `reason = STOP_PANEL`, a VM stop code, so there is no 250 ms sleep), writes the register block under a seqlock every 64 samples, and adapts `interval` every 1024 samples so that samples/s ≈ `target_rate`.

Layout (little-endian; offsets verified with `offsetof`):
```
0   magic 'P11P', version 1, size, ring_size(4096)
16  ring_wr (u32, monotonically increasing)   20 state 0=halt 1=run 2=wait   24 seq (seqlock)   28 halt_count
32  r[8] (R0-R5,SP,PC)   64 psw  68 dr  72 mmr0  76 mmr3  80 last_addr  84 last_data
88  sim_time (double)    96 sample_interval  100 samples_per_sec
104 sr  108 sr_valid  112 halt_req  116 target_rate      (bridge -> sim)
184 ring[4096] of {u32 addr; u16 data; u16 pc}           total 32952 bytes
```

**Measured cost** [V], using `exp6_bench.py`, median of 4 interleaved `STEP n` runs:

| Variant | SOB loop | Memory-copy mix |
|---|---|---|
| Unpatched | 171.6 MIPS | 118.7 MIPS |
| Patched, detached | 160.9 (-6%) | 108.8 (-8%) |
| Patched, attached at 50k samples/s | 159.9 | 110.8 |
| Patched, attached at 1M samples/s | 157.2 | 110.8 |

Almost all of the cost is the per-access stores and the per-instruction countdown; the sampling itself is negligible. This works out to about 1-2 host cycles per instruction.

The rate adaptation held [V] 50001 samples/s (interval 1804) and 982637 samples/s (interval 88). Real 11/70 speed is below 1 MIPS, so if you throttle the cost is irrelevant.

**Measured behaviour** [V], `exp7_bridge.py` and `exp7b_idle_panel.py`:
* **60 Hz frames** carried about 480-730 samples each at 30k/s. The data-lamp duty reproduced opcode 077301 (SOB), and the address lamps showed 001062.
* **Live SR.** `sr=100000` became visible in the guest's R5 after 1.15 ms, with no halt.
* **WAIT** frames are flagged (`state=2`, PC 001054), and DR steps at 60 Hz.
* **Halt.** A `halt_req` increment produced `\nFront panel HALT, PC: 001062 (SOB R3,1062)\nsim> ` on stdout after **0.10 ms**.
* **Idle.** With the panel attached and `SET CPU IDLE=2` in a WAIT loop, host CPU was **6.0%**.

**Refinements for production** [U]:
* Rate-limit WAIT samples to one per idle wake-up.
* Also record the virtual address and space for the USER/SUPER/KERNEL D/I knob positions. Hook ReadW, ReadB, WriteW, WriteB, ReadMW and ReadMB, which have `va`; that costs one more store.
* Optionally capture "address error" and "parity" events, and `last_pa` for read-modify-write.
* Publish the TTO and RL/RP activity bits for the panel.
* Fix B2 and B4 upstream if the remote console is still used.
* Note on the WAIT display (memory, not verified): on a real 11/70, during WAIT the data lights reportedly show R0 in DATA PATHS, which is how the RSX idle pattern becomes visible. That is why the WAIT samples carry R0 in `data`.

### 5.3 Bridge design (concrete)

**Process model.** In each container, VM or LXC, a supervisor (systemd or `docker --restart`) runs **one bridge process**. The bridge `exec`s `pdp11 boot.ini` with **stdin/stdout pipes**.

**Startup.**
* The bridge creates the shm file on tmpfs (`/dev/shm/pdp11-panel`, or a shared volume if the web tier is in another container).
* It runs SimH in a writable working directory, which the temp log needs.

**Example `boot.ini`:**
```
set cpu 11/70
set cpu 4M
set cpu idle                ; or: set throttle 1M  (mutually exclusive)
set console -u telnet=127.0.0.1:2323;notelnet
set console telnet=buffered=65536
set tto 7b                  ; or 8b
attach panel /dev/shm/pdp11-panel
set dz enabled              ; optional extra terminals, localhost only
attach -u dz 127.0.0.1:2324;notelnet
attach rp0 rsx11m.dsk       ; disks...
```
Do **not** `set remote master`, and do not enable the remote console unless it is needed for debugging.

**Channels.**
* **Console.** The bridge connects to `127.0.0.1:2323` and relays bytes to and from the browser's xterm.js over WebSocket. With NOTELNET there is no IAC parsing. Reconnects get the buffered tail. A `^E` from the browser goes to the guest.
* **SCP.** The bridge writes one command per line to stdin and reads stdout up to `sim> `.
  * While halted, commands complete in about 0.1 ms; batch them freely.
  * While running, SCP does not read stdin. Only `halt_req` (shm) or SIGINT (fallback, +250 ms) brings it back.
  * Parse stop lines with the regex `^(?P<why>.*), PC: (?P<pc>[0-7]+) \((?P<insn>.*)\)$`. Reasons seen: "Simulation stopped", "HALT instruction", "Step expired", "Front panel HALT", trap names from sim_stop_messages, "Breakpoint", and "Unit not attached".
* **Panel.** At 60 Hz the bridge:
  * reads `ring_wr` and consumes the new entries;
  * computes per-lamp duty for the 22 ADDRESS and 16 DATA lamps, plus the mode lamps from the flags;
  * reads registers with the seqlock;
  * picks a display per the knob: PROG PHY uses the bus addr; USER/SUPER/KERNEL D/I use the virtual address (after the refinement); DATA PATHS uses bus data, or R0 during WAIT; DISPLAY REGISTER uses `dr`.

  Apply an exponential decay on the client side to imitate incandescent persistence. When halted, display CONS PHY / the console address and the EXAM/DEP data.

**Front-panel switches mapped to actions:**

| Switch | Action |
|---|---|
| SR switches | `sr` = low 16 of the 22 switches, `sr_valid=1`, live. The bridge keeps all 22 bits for LOAD ADRS. |
| LOAD ADRS | bridge-side: `cons_addr = switches<21:0>` |
| EXAM / DEP | halted only: `examine -h? {cons_addr}` (physical, or `-v -k/-s/-u [-t]` per the knob), then `deposit {cons_addr} {sw}`, auto-incrementing by 2 on repeated EXAM/DEP (I/O-page registers work, [V]) |
| HALT/ENABLE to HALT | increment `halt_req`, wait for `sim> ` |
| CONT | `cont` |
| S INST / S BUS CYCLE | `step` (bus-cycle granularity is not available; map it to `step`) |
| START | `reset` then `go {cons_addr}`, or `run {cons_addr}` |
| BOOT | `boot rp0` |
| Attach/detach media | `attach` / `detach` while halted. While running, either do a quick halt plus command plus `cont`, or use a non-master remote session in single mode, which allows ATTACH/DETACH while running (per the table at :826-845; not tested). |

**Fallback without the patch** (if the next phase refuses to patch):
* Do exactly the above, minus the shm.
* Lamps come from a non-master remote session (`set remote -u telnet=127.0.0.1:p`, `set remote connections=2`, plus `set remote timeout=0`) running `REPEAT EVERY 16667 USECS EXAMINE PC,PSW,R0,WAIT;EXAMINE SYSTEM DR`, parsed once per frame with the bridge doing temporal smoothing. Avoid COLLECT if idle matters.
* SR changes use SIGINT → `deposit system sr x` → `cont` (about 300 ms per change).
* Filter the `Remote Console Command from ...` lines out of stdout.
* Never send `^E` to that session (B2).

---

## 6. Files

* Report (this file): `$SCRATCH/research/r1-simh-control.md`
* Stock source and binary: `$R/simh`, `$R/simh/BIN/pdp11`. Build log: `$R/build-pdp11.log`.
* Patched prototype: `$R/simh-panel` (`PDP11/pdp11_panel.c`, `PDP11/pdp11_panel.h`), `$R/simh-panel/BIN/pdp11`. Diff: `$R/exp/panel-prototype.diff`.
* Harness and experiments: `$R/exp/simhlib.py`, `asm11.py`, `exp1*.py` … `exp9_pipeline.py`, `exp7_bridge.py` (shm reader with seqlock and ring-duty code), `fp_pdp11.c`, `offsets.c`, and the raw transcripts `$R/exp/*.transcript.txt`.

# R2: PDP-11/70 front panel, behavioral spec and how SimH replicas implement it

Researcher: r2-realcons. Research phase only. Nothing was written to /home/kim/dev/repos/pdp11-emu.

Legend used throughout:
- **VERIFIED (src)**: I read it in the cited source file or line.
- **VERIFIED (doc)**: I read it in the DEC document text (OCR) or saw it in a schematic or photo.
- **VERIFIED (exp)**: I ran it here. The transcript is in section 8.
- **UNVERIFIED / INFERRED**: from memory or reasoning. Treat as a hypothesis.

## 0. Sources (all local copies are under `$SCRATCH/r2-realcons/`)

`$SCRATCH` stands for the research session's scratch directory, an ephemeral working directory under `/tmp` that no longer exists.

Abbreviations used below:

| Abbrev | What | Local path (relative to r2-realcons/) | Version |
|---|---|---|---|
| BB | BlinkenBone (Joerg Hoppe) | `dl-blinkenbone/BlinkenBone/` | master = tag 1.7, commit 1ce5c065 (2019-02-03) |
| RC | REALCONS 11/70 logic (BB tree) | `dl-blinkenbone/BlinkenBone/projects/02.3_simh/4.x+realcons/src/REALCONS/realcons_console_pdp11_70.c` | same |
| BBSIM | BB's patched SimH 4.0 | `dl-blinkenbone/BlinkenBone/projects/02.3_simh/4.x+realcons/src/` (`scp.c`, `PDP11/pdp11_cpu.c`) | SimH 4.0-0 + REALCONS |
| JAVA | BB Java panel simulator, 11/70 | `dl-blinkenbone/BlinkenBone/projects/09_javapanelsim/src/blinkenbone/panelsim/panelsim1170/Panel1170.java` | same |
| PI | PiDP-11 software (Oscar Vermeulen) | `dl-pidp11/pidp11/` | main, commit 26d20bed (2026-04-10) |
| PISRV | PiDP-11 Blinkenlight server ("server11") | `dl-pidp11/pidp11/src/11_pidp_server/pidp11/{main.c,gpio.c,gpiopattern.c,gpiopattern.h}` | same |
| HB | PDP-11/70 Processor Handbook 1977-78, ch. 11 "Console Operation" | `dl-dec-PDP-11_70_Handbook_1977-78/` (`text.txt` lines 9585-9967) | bitsavers `pdf/dec/pdp11/1170/` |
| KB | KB11-C Processor Manual EK-KB11C-TM-001, Section III (Console) | `dl-dec-EK-KB11C-TM-001_1170procMan/` (`text.txt` lines 11100-11730) | bitsavers |
| SCH | 11/70 console board schematic D-CS-5411294-0-1 (KNLA-KNLD) | `dl-dec-1170_ctlPanelSchem/1170_ctlPanelSchem.pdf` (sheet 5 = KNLD) | bitsavers |
| OS | open-simh, current master | `dl-opensimh/simh/` | commit 87eb7d5e (2026-09-22) |
| PIMAN | PiDP-11 manual, 2024-07-30 | `dl-pidp11-manual/PiDP-11_Manual.pdf` | obsolescence.dev |

Notes on the sources:
- The PI copy of the SimH tree and the REALCONS code is the same as BB. There is one functional change, the power-off handling in `realcons_console_pdp11_70.c:774-795` (see 4.6). PI's `scp.c` links readline statically. Its hook in `pdp11_cpu.c` differs only by one `extern`.
- `REALCONS/realcons_pdp11_70.txt` (BB, 524 lines) is useful in its own right. Lines 1-39 are Joerg's notes from tests on the real 11/70 "Miss Piggy" at the Living Computers Museum (LCM) in 2018. Lines 41-525 are his transcription of KB Section III. I checked that transcription against the KB OCR and it is faithful. The OCR of the DEC Table 2-1 PROG PHY row says "VA(06:15)". Joerg corrects this to PA, and the KB body text supports him ("In PROG PHY, PA(21:06) are displayed").

---

## 1. Lamps: complete inventory (real 11/70 vs replicas)

The real panel has 64 indicators. Sources: VERIFIED (doc) from SCH KNLD sheet, KB III-1/III-2, HB 11.2, and the panel photo used by the BB Java panelsim (`.../panelsim1170/images/pdp1170_size=1200_background.png`). The PiDP-11 also has 64 LEDs: PISRV `main.c:612-661` plus the knob LEDs.

| # | Panel label (as printed) | Bits | DEC meaning | DEC driving signal (KB III-2-6, §2.12) | Notes |
|---|---|---|---|---|---|
| 22 | ADDRESS 21..0 | 21:00 | Address of the data being examined or deposited, or of the current bus cycle. Virtual or physical, depending on the ADDRESS knob (see 3.2) | KNLB VA(03:00), KNLB DISP ADRS(21:04) | Colour bands in octal groups. Bit 21 is leftmost. |
| 16 | DATA 15..0 | 15:00 | Output of the data display multiplexer, selected by the DATA knob | PDRF DISP D(15:00) | |
| 2 | PARITY HIGH, LOW (left of the DATA row) | | Parity bits of the high and low byte of the word read from cache. **Off during writes** | PDRH IND HI/LO PAR. Clocked at T4 after the Pause of every cache DATI/P. Cleared at T4 of PAUSE of every Unibus cycle and every cache DATO/B (KB 2.11) | |
| 1 | PAR ERR | | Unibus or memory parity error flagged | UBCB IND PAR ERR | |
| 1 | ADRS ERR | | NXM, access control violation, page length error, stack limit red, odd address, Unibus timeout. **Dynamic** during program execution, **static** after a console EXAM or DEP (KB 1.3.2) | SCCF IND ADRS ERR | HB lists NXM, ACV, unassigned page |
| 1 | RUN | | The CPU executes instructions. Off during Pause cycles. **On during WAIT** (KB 1.3.3, HB 11.8.9) | TMCF IND RUN | |
| 1 | PAUSE | | On during every Bus Pause and Interrupt Pause, while the CPU waits for memory or a Unibus device | TMC IND PAUSE | |
| 1 | MASTER | | The CPU is Unibus master (UBCA CPBSY), **or a console operation is in progress** (TMCA CONF (1), the console flag) | UBCF IND MASTER | HB: "The CPU relinquishes control of the UNIBUS during DMA and NPR transfers" |
| 3 | USER, SUPER, KERNEL | | The mode actually used in each cycle | Console-board decode of SSRB MMR0 MODE 0/1 | The mode code 10 (illegal) shows no lamp |
| 1 | DATA | | The last memory reference was to D space (on) or I space (off) | SAPK IND DATA | |
| 3 | ADDRESSING 16, 18, 22 | | Mapping used in each cycle: 16-bit if MMR0<0>=0; 18-bit if MMR0<0>=1 and MMR3<4>=0; 22-bit if both are 1 (KB IV-1-1, VERIFIED (doc)) | SCCF IND 16/18/22 BIT MODE | 16-bit mapping is set after power-up, Console START, or a RESET instruction |
| 8 | ADDRESS knob position lamps. These are backlit labels in a 2x4 grid left of the knob. Left column: USER D, SUPER D, KERNEL D, CONS PHY. Right column: USER I, SUPER I, KERNEL I, PROG PHY | | Show the knob position | **Driven directly by the switch** (KB III-1-3, III-2-6). SCH KNLD: S32 contacts feed 7417 buffers that drive the LEDs | **They exist on the real panel.** |
| 4 | DATA knob position lamps in a 2x2 grid. Top row: DATA PATHS, µADRS FPP/CPU. Bottom row: BUS REG, DISPLAY REGISTER | | Show the knob position | Driven directly by switch S33 | |

How the replicas handle these lamps:
- REALCONS (SimH side) computes everything except the 12 knob-position lamps. Those lamps are lit locally by the panel server:
  - PISRV: `main.c:199-205`, `gpiopattern.c:289-394`.
  - JAVA: `syncKnobsFeedbackLeds()`, `Panel1170.java:715-736`.
- The Java comment "not on physical panel" (Panel1170.java ~141) means "this is not a Blinkenlight-API control". The lamps do exist on the real panel, as stated in KB and shown by the photo and SCH.
- PiDP-11 mapping for USER/SUPER/KERNEL: the API value 0=K, 1=off, 2=S, 3=U is converted to a one-hot LED pattern (PISRV `main.c:216-232`).

LAMP TEST lights every indicator, including the 12 knob lamps. SCH KNLD shows diodes from each LED to the KNLD LAMP TEST L line, and PISRV lights all of them (`gpiopattern.c:323-327, 376-380`). Reading of the diodes is VERIFIED (doc), low confidence, low-resolution schematic.

---

## 2. Switches, knobs, key switch

### 2.1 Switch list (VERIFIED (doc): KB III-1-1..4, HB 11.2/11.8; actuation direction also from the JAVA active-state images)

| Control | Type | Direction / active position | Function summary |
|---|---|---|---|
| SR 0..21 | 22 latching toggles | up = 1 (JAVA uses `switch_sr_NN_up.png` as the "1" image) | Address/data entry. Only bits 15:00 are program-readable, at 17 777 570 |
| LAMP TEST | Momentary. White, **unlabeled** switch between SR0 and LOAD ADRS | **Raised** = test (KB: "When raised, it turns all the indicators on"; HB 11.8.4) | Hardware-local. It does not reach the CPU logic |
| LOAD ADRS | Momentary | depressed (down) | Loads SR<21:16> into SCCK SWR(21:16), and SR<15:00> into PCA and the CPU "SR" register |
| EXAM | Momentary | depressed | Examine. Repeated presses step to the next location |
| DEP | Momentary | **raised (up)** (KB 1.1.4 "When it is raised"; JAVA `switch_dep_up.png`, Panel1170.java:401) | Deposit SR<15:00>. Repeated presses step |
| CONT | Momentary | depressed | ENABLE: resume. HALT: single instruction or single bus cycle |
| ENABLE/HALT | 2-position latching | up = ENABLE, down = HALT (HB 11.3 "ENABLE (up)... HALT (down)") | Stop or allow execution |
| S INST / S BUS CYCLE | 2-position latching | up = S INST, down = S BUS CYCLE (JAVA active image `switch_sinst_sbuscycle_down.png`) | Granularity of CONT while in HALT. No effect in ENABLE |
| START | Momentary | depressed | ENABLE: INIT, then run at the loaded address. HALT: system reset (INIT) only. No effect while running |

Debounce: S24-S30 (LOAD ADRS..START) "are input to latches (KNLC) for bounce suppression" (KB III-2-1). VERIFIED (doc).

### 2.2 ADDRESS SELECT knob (S32, 8 positions)

DEC code select lines KNLD DISP ADRS SEL2..0. These are VERIFIED (doc), read from the table printed on SCH KNLD sheet 5. I cropped it at 200 dpi (section 8.4):

| Position | A2 A1 A0 | REALCONS value (RC:76-83) = DEC code with L=1 |
|---|---|---|
| USER I | H H H | 0 |
| USER D | H H L | 1 |
| SUPER I | H L H | 2 |
| SUPER D | H L L | 3 |
| CONS PHY | L H H | 4 |
| KERNEL I | L H L | 5 |
| KERNEL D | L L H | 6 |
| PROG PHY | L L L | 7 |

Physical clockwise detent order, as implemented by both replicas:

PROG PHY → CONS PHY → KERNEL D → SUPER D → USER D → USER I → SUPER I → KERNEL I → (wraps to PROG PHY)

Evidence:
- PISRV help text "clockwise from 0=PROG PHY .. 7=KERNEL I" (`main.c:430-432`) and the map `knobAddrMap={7,4,6,3,1,0,2,5}` (`main.c:97-98`).
- JAVA state images (`Panel1170.java:574-581`) and the table at `Panel1170.java:1030-1050`.
- This order walks the 2x4 label grid clockwise. VERIFIED (doc) from the photo: bottom-right PROG PHY, bottom-left CONS PHY, up the left column, across the top, down the right column.
- Both replicas wrap continuously: PISRV `gpio.c:319-320` uses `knobValue[0] & 7`; JAVA "rotate over 0" at `Panel1170.java:900,906`. SCH draws S32 as an 8-contact switch with a circular "C.W." arrow. Whether the real switch has a mechanical end stop is **UNVERIFIED** (my inference: no stop).

Defaults: PISRV starts with ADDR = CONS PHY and DATA = DATA PATHS (`main.c:94-95`). JAVA defaults are set on its command line.

### 2.3 DATA SELECT knob (S33)

DEC code lines DISP DATA SEL1/SEL0. VERIFIED (doc), SCH KNLD:

| Position | SEL1 SEL0 | REALCONS value (RC:86-89) |
|---|---|---|
| BUS REGISTER | L L | 3 |
| DATA PATHS | L H | 2 |
| DISPLAY REGISTER | H L | 1 |
| µADRS FPP/CPU | H H | 0 |

- S33 is an **8-contact** rotary switch whose contacts are wired in pairs, so the 4 functions each occur twice, 180° apart. This is VERIFIED (doc) by reading the SCH crop: contacts 1/5, 2/6, 3/7, 4/8 tie to the same lines.
- JAVA agrees: "DATA knob has 8 positions, but encodes 0,1,2,3,0,1,2,3" (`Panel1170.java:583-594`).
- PISRV uses `knobValue[1] & 3`.
- Clockwise order: BUS REG → DATA PATHS → µADRS FPP/CPU → DISPLAY REGISTER → (BUS REG ...). This is PISRV help "clockwise from 0=BUS REG .. 3=DISPLAY REGISTER" (`main.c:433-435`) and map `knobDataMap={3,2,0,1}` (`main.c:100-101`). It walks the 2x2 label grid clockwise.

### 2.4 Key switch (S31): OFF / POWER / LOCK

The photo shows OFF at lower-left, POWER at upper-left and LOCK at upper-right of the key.

- **OFF**: removes power from the switched outlets of the power controller.
- **POWER**: all switches work.
- **LOCK**: same as POWER, but LOAD ADRS, EXAM, DEP, CONT, ENABLE/HALT, S INST/S BUS CYCLE and START are disabled. SR, LAMP TEST and both knobs still work (KB 1.1.1, HB 11.8.1).
- How LOCK works in hardware: S24-S30 lose their switched ground. In addition, "KNLA PNL LOCK L ... **forces the HALT/ENABLE switch output to the ENABLE logic value**. Thus, when the power switch is in the LOCK position, the processor is enabled, the HALT switch is inoperative" (KB III-2-1). VERIFIED (doc).
- REALCONS implements LOCK exactly like this: with `panel_lock`, `console_halt` is forced to 0 and the action switches are ignored (RC:784, 808-811, 815).

### 2.5 PiDP-11-only controls (not on a real 11/70)

These are VERIFIED (src) and PIMAN p.12-14.
- **Pushing the ADDRESS knob** maps to API control "POWER" (row 1, bit 10; PISRV `main.c:136-165, 665`):
  - HALT=0: runs `/opt/pidp11/bin/rebootsimh.sh`. It writes "exit" into `/dev/shm/pidp11/tmpsimhcommand.txt`. SimH executes it, and `bin/pidp11.sh` restarts SimH, booting the system whose octal number is set in SR (via `scansw`, `pidp11.sh:24-37`, `systems/selections`).
  - HALT=1: runs `down.sh` (`sudo shutdown -h now`).
- `-L` gives a permanent PANEL LOCK (`main.c:474-476`).
- `-a`/`-d` set the start positions of the knobs.

---

## 3. Behavior spec

### 3.1 Processor states as seen on the console (VERIFIED (doc): KB III-2-2..5, HB 4-79..4-81 and 11.3)

- **Halted (console mode)**: the microcode loops in CON.00. The KB text gives UADR field 070 with bit 6 forced, so the address is 0170. CON.00 loops until a console switch sets CNSL ACT. Halt sources:
  - The HALT switch. It sets the console flag at the next BRQ strobe, so the current instruction completes.
  - A HALT instruction in **kernel** mode. In super/user mode it traps to 4 (KB II-6-11 "Illegal Halt").
  - START with HALT (console reset).
  - All halts pass through BRK.00 (Flows 12), which copies **PC → SR**. This is Joerg's reading of the flows (RC:323-324, 386-387, 399-400). UNVERIFIED (flows not checked).
- **HALT instruction** (HB 4-79): "The console data lights display the contents of R0; the console address lights display the address after the halt instruction ... No INIT." VERIFIED (doc).
- **WAIT** (HB 4-80): the PC points past the WAIT. RUN stays **on** (HB 11.8.9). On the real machine the data path (DATA PATHS) shows **R0** during WAIT:
  - Joerg's FLOWS 3 reading: "WAIT: ADRS light = PC, Shifter = DR = R0" (`realcons_pdp11_70.txt:32-39`).
  - Paul Koning (cctalk, 2016-05): "data paths ... was some internal state except during a WAIT instruction, when it would show the contents of R0".
  - LCM observation: PAUSE is **not** lit during WAIT (`realcons_pdp11_70.txt:7-9`).
- **RESET instruction**: INIT for 10 ms (HB 4-81, table "RESET 10ms"). LCM: RUN is **on** during RESET. R0 goes to DR/shifter (FLOWS 3, per Joerg).
- **Running**: RUN on but off during Pause cycles, so it looks slightly dim. PAUSE flickers with cache misses and Unibus waits. LCM saw "PAUSE ON during disk I/O ... then RUN is OFF" (`realcons_pdp11_70.txt:7-9`).
- **MASTER**: on when halted (console flag). While running it is mostly off. LCM: "ON at HALT, OFF at RUN" (`realcons_pdp11_70.txt:11-13`). INFERRED reconciliation with the DEC text: the 11/70 reaches memory through the cache/memory bus, so the CPU is only Unibus master during I/O-page cycles. MASTER should therefore be a faint flicker, proportional to I/O-page traffic.

### 3.2 ADDRESS lamps per knob position

The DEC table below is VERIFIED (doc): KB Table 2-1 and §2.10, with the PROG PHY row corrected as discussed in section 0.

| Display bits | VIRTUAL (6 positions) | CONS PHY | PROG PHY | General Register address loaded (any knob) |
|---|---|---|---|---|
| 00-03 | VA(00:03) | VA(00:03) | VA(00:03) | VA(00:03) = register number |
| 04-05 | VA(04:05) | VA(04:05) | VA(04:05) | **OFF** |
| 06-15 | VA(06:15) | VA(06:15) | PA(06:15) | **ON** |
| 16-21 | **OFF** | SWR(16:21), latched at the last LOAD ADRS | PA(16:21) | **ON** |

What this means:
- **Running.** VIRTUAL shows the VA of each bus cycle. CONS PHY shows the same VA, with bits 21:16 frozen at the last LOAD ADRS value. PROG PHY shows the 22-bit PA of each cycle. All six VIRTUAL positions look the same while running; the I/D and K/S/U choice only matters for console EXAM and DEP.
- **Halted.** VIRTUAL and CONS PHY show the console address register SR (SR<15:00>, plus SWR<21:16> in CONS PHY). That is the LOAD ADRS value, the stepped address, or the PC after a halt or step. PROG PHY shows the PA of the last bus cycle. HB 11.4: after an EXAM or DEP on a virtual address, turning the knob to PROG PHY shows the relocated physical address. "Using the PROG PHY position in any other way will produce meaningless results."
- **General registers.** When LOAD ADRS loads 17 777 700-17 777 717, the hardware sets SCCF GEN REG and forces the pattern shown in the last column. The display then reads 177777 0n or 177777 1n **regardless of the knob**, including in VIRTUAL, where bits 21:16 are normally off (KB 2.10.1).

### 3.3 DATA lamps per knob position

| Knob | Running | WAIT | Halted | After EXAM / DEP | After S INST step |
|---|---|---|---|---|---|
| DATA PATHS (shifter output; the "normal" position) | ALU/shifter output of each micro-cycle: a blur | R0 | After a HALT instruction: R0. After the HALT switch: microcode-dependent | The data examined or deposited | "Not necessarily meaningful" (HB 11.7); "may only be determined by examination of the microprogram flows" (KB 1.1.8) |
| BUS REG (BR) | Data of the bus cycles | (BR) | **SR<15:00>, live**. KB 2.8: "During CON.00, bits 15-00 of the Switch Register are loaded into the BR" | SR<15:00> | SR. During an S BUS CYCLE stop: the data read or written by that cycle |
| µADRS FPP/CPU | Bits 15:08 = FPP µPC, 07:00 = CPU µPC; a blur | WAIT state | CPU µPC = 170 (CON.00) | | |
| DISPLAY REGISTER | DR: the last word a program wrote to 17 777 570 | DR | DR | DR | DR |

Sources: VERIFIED (doc) KB 1.2.3, 2.8, 2.11; HB 11.8.7.

### 3.4 Console operations: exact rules

All of these are VERIFIED (doc) from KB III-1 and HB 11.3-11.8 unless marked otherwise.

1. **LOAD ADRS** (only when halted):
   - SR<21:16> goes to SWR(21:16). SR<15:00> goes to PCA and the CPU "SR" (via BR during CON.00, ADR.00 at µaddress 270).
   - The ADDRESS lamps then show the value as filtered by the knob (VIRTUAL: bits 21:16 off).
   - It ends any EXAM or DEP step sequence.
   - It clears ADRS ERR. This is an LCM observation, coded in RC:900-901.
   - LCM also saw the DATA PATHS display cleared to 0 after LOAD ADRS (RC:898).
   - It does **not** change the PC used by CONT. HB: CONT resumes "from the point at which it was halted".
2. **EXAM**:
   - Reads the location at the console address. VIRTUAL: SR<15:00> is a VA, relocated with the PAR/PDR set the knob selects, if memory management is on. CONS PHY: the 22-bit address. During console cycles the knob also selects the MMU mode (KB IV-3-3, ROM OUT12 "SSRK CNSL KERNEL/SUPER/USER").
   - The data appears in DATA when the knob is on DATA PATHS.
   - **Step rule**: an EXAM directly after an EXAM first increments the address, then examines. The increment is **+2** for memory, **+1** inside 17 777 700-717, and wraps 17 777 717 → 17 777 700.
   - Stepping from 17 777 676 to 17 777 700 is **not possible**.
   - "An EXAM-STEP or DEP-STEP operation will not cross a 32K word memory block boundary" (HB 11.5). Only the low 16 bits count up. REALCONS wraps within the 64 KB block; whether the hardware wraps or stops is UNVERIFIED.
3. **DEP**:
   - Writes SR<15:00> to the console address and shows the data in DATA PATHS.
   - A DEP directly after a DEP increments first.
   - EXAM and DEP are "coupled": a DEP after an EXAM (or an EXAM after a DEP) works on the **same** location. DEC's sequence (HB 11.5, KB 1.1.5):

     `LOAD ADRS→X, EXAM→X, DEP→X, EXAM→X, EXAM→X+2 (EXAM-STEP), DEP→X+2, EXAM→X+2`

   - The address on display is the location just accessed; it does **not** pre-increment after a DEP. PIMAN p.13 ("the ADDRESS leds have auto-incremented for your next word") is wrong on this point, both for DEC and for REALCONS.
4. **Errors**:
   - ADRS ERR is static after a failing console EXAM or DEP (NXM etc.). "An EXAM or DEP operation which causes an addressing error (ADRS ERR or PAR ERR) will be aborted and must be corrected by performing a new LOAD ADRS operation with a valid address" (HB 11.4 NOTE).
   - LCM 2018 (`realcons_pdp11_70.txt:23-31`): EXAM at 17 777 754 (NXM) changes the ADDRESS display to 17 600 000, so bits 15:0 are cleared. While stepping, the ADDRESS display does not change on the error.
   - Console EXAM at an **odd** address on real hardware: UNVERIFIED. REALCONS clears bit 0 (RC:230). SimH ignores bit 0 (section 8.1).
5. **CONT**, only when halted:
   - ENABLE: resume at PC.
   - HALT + S INST: execute exactly one instruction, then CON.00, so the ADDRESS display shows the new PC.
   - HALT + S BUS CYCLE: run until T5 of PAUSE of the next Unibus or memory cycle. ADDRESS shows that cycle's address (virtual or physical per knob). With BUS REG selected, DATA shows the data read, or the data written (except on stack operations and FP instructions).
   - In S BUS CYCLE mode, **LOAD ADRS, EXAM and DEP do nothing** and **NPRs are disabled** (UBCF DISABLE NPR L). To get back: switch to S INST and press CONT once, which finishes the current instruction.
   - CONT has no effect while running.
6. **HALT switch** (ENABLE→HALT while running): stops at the end of the current instruction. Contents are kept. Returning the switch to ENABLE has no effect. HB note: "NPR's are still serviced after a halt from the console if S BUS CYCLE is disabled."
7. **START**, only when halted:
   - ENABLE: sets PS<15:14>=kernel, then RESET WAIT 100 µs, then INIT for 10 ms, then fetches and executes at the **displayed (loaded) address** (KB 2.7.3).
   - HALT: console reset. All three INITs are generated and the console flag stays set (KB 2.6.4).
   - START has no effect in RUN state.
   - After a Console START the mapping is 16-bit, so the start address is effectively a 16-bit VA (low 56 KB, plus the I/O page at 160000-177777).
8. **Program interface** (KB 1.1.10, 1.2.3; HB 11.8.3/7):
   - **Reads** of 17 777 570 return SR<15:00>. This register is read-only.
   - **Writes** to 17 777 570 load the Display ("Light") Register, which is write-only. The DR is shown only with the DATA knob on DISPLAY REGISTER.
   - SR<21:16> is not program-visible.
9. **Parity lamps**: set from the cache parity bits on cache read cycles (DATI/P). Cleared on writes and on **all Unibus cycles**, which includes every I/O-page access (KB 2.11, HB 11.8.8).
   - LCM: "not always in sync with DATA LEDs. Only on Memory READ, only if BUS REG"; "Both OFF on manual DEPOSIT & EXAM ???" (`realcons_pdp11_70.txt:16-20`).
   - Parity sense: REALCONS lights the lamp when the byte has an even number of ones, i.e. odd parity (RC:1088-1092). The real 11/70 sense is UNVERIFIED (likely odd parity).

### 3.5 OS idle patterns: what shows up where

| OS | Mechanism | Pattern (from the code) | Knob needed | Evidence |
|---|---|---|---|---|
| RSX-11M / M-PLUS | Executive idle loop in `[11,10]SYSXT.MAC`. `MOV (R1),R0 ; ... ; WAIT`. The pattern is in **R0 during WAIT**. Stock RSX does **not** write 177570. Mark Matlock's QProbe patch replaces two NOPs at the label `QPROBE` with `MOV R0,@#177570` | `ASLB (R1)+ ; RORB (R1) ; ADCB -(R1)` on word `$IDLPT`, every 3rd pass (`$IDLCT` reset to 2). The low byte shifts left (right half moves toward the center) and the carry enters bit 15. The high byte rotates right (left half moves toward the center) and its carry goes to bit 0. So the lights converge on the centre from both edges and re-enter at the outer edges ("starts from each side ... moves to the center where it then explodes outward", retrocmp story 203). gunkies.org says "swept outwards"; the code says inward motion with wrap | DATA PATHS | Code as quoted at retrocmp.com/tools/qprobe/339 and /353. Secondary source; the RSX source was not obtained. Initial `$IDLPT` value: UNVERIFIED |
| 2.11BSD | `idle()` in `sys/pdp/mch_xxx.s`: `mov rdisply,r0 / wait displays contents of r0 ; SPLLOW ; wait`. No display-register write anywhere in kern_clock.c, machdep.c or machdep2.c | `rdisply` starts at 0377 and is rotated every 2nd `idle()` call: `clc; rol rdisply; bpl 1f; bis $1,rdisply`. Result: a snake of 8-9 lights rotating **left** (sequence in 8.3) | DATA PATHS | VERIFIED (src), TUHS `2.11BSD/sys/pdp/mch_xxx.s` |
| IAS | Idle loop: `mov 3022,r0 ; dec 3020 ; wait`. Every 4th clock tick: `clc ; bmi 1f ; sec ; 1: rol r0`, stored back | A bar fills from the right to all 16 lights, then empties from the right, so it sweeps left. With timesharing (TS) started it reportedly rotates right | DATA PATHS | Lee Gleason's E11 trace in BB `REALCONS/pdp1170_idle_pattern.txt:9-65` |
| RSTS/E | R0 during WAIT ("That was where RSTS would put its idle lights pattern"). A "fancy" pattern is an undocumented SYSGEN option | not documented here | DATA PATHS | P. Koning, cctalk 2016-05/022702 and 2018-12/044690 |
| RT-11 (SYSGEN "idle loop light pattern", FB/XM) | Writes the pattern word to `SR_` (`MOV (PC)+,@(PC)+ ; .WORD pattern,SR_`). `SR_` is presumably 177570, i.e. the **Display Register** (UNVERIFIED) | A "migrating bar": ROL until a bit falls off, then reverse, with a COM when the pattern is clear. Advances once every 512 idle-loop passes, so the speed depends on CPU idle speed | DISPLAY REGISTER | cctalk 2017-12/036658 (code snippet) |
| Unix V6 and V7 | `clock()` calls `display()` every tick (V6 `ken/clock.c`, V7 `sys/clock.c`). `_display` in `m45.s` / `mch.s`, with `CSW = 177570`: reads the **switches as an address** (odd means user/previous space), fetches that word, writes it to 177570. 2 s back-off after a fault | Shows the memory word selected by the switches | DISPLAY REGISTER | VERIFIED (src), TUHS |
| PiDP demos | Mike Hill's IDLED (boot 0000/1001) imitates the RSX pattern (start 170017) and the IAS one. It also writes the DR (`152 DEC -(R1); 154 MOV (R1)+,(R4)`). BLINKY uses PROG PHY, DATA PATHS and DISPLAY REGISTER | | | PI `systems/idled/boot.ini:125-216`; BB `02.0_simh.machines/pdp1170-blinky/` |

For the RSX and 2.11BSD patterns, the ADDRESS lamps sit almost constantly on the PC after the WAIT. They are bright and steady, with short flickers when interrupt code runs. RUN and KERNEL are lit. ADDRESSING shows 22 on a 22-bit 11/70 system (INFERRED).

---

## 4. How REALCONS and the PiDP-11 map this onto SimH

### 4.1 Architecture

```
SimH (patched scp.c + pdp11_cpu.c) --REALCONS console logic "11/70"--
   | ONC-RPC "Blinkenlight API" (rpcgen, TCP/UDP, portmapper)
   v
panel server: PiDP-11 "server11" (GPIO mux)  |  BB blinkenlightd (BlinkenBus HW)  |  BB Java panelsim
```

- The panel is called "11/70". Its controls, with names and widths from `Panel1170.java:239-317` and the REALCONS look-ups at RC:628-693:
  - Inputs: `POWER`(1), `PANEL_LOCK`(1), `SR`(22), `LOAD_ADRS`, `EXAM`, `DEPOSIT`, `CONT`, `HALT`, `S_BUS_CYCLE`, `START` (1 each), `ADDR_SELECT`(3), `DATA_SELECT`(2).
  - Outputs: `ADDRESS`(22), `DATA`(16), `PARITY_HIGH`, `PARITY_LOW`, `PAR_ERR`, `ADRS_ERR`, `RUN`, `PAUSE`, `MASTER`, `MMR0_MODE`(2: 0=K, 1=off, 2=S, 3=U), `DATA_SPACE`, `ADDRESSING_16/18/22`.
  - LAMP TEST and the knob lamps are panel-local. PISRV additionally exposes `LAMPTEST` and `*_SELECT_FEEDBACK`.
- SimH commands: `set realcons host=..`, `panel=11/70`, `interval=<ms>`, `connected|disconnected`, `test` (3 s lamp test), `debug` (`realcons_simh.c:36-48`). `set cpu 11/70` sets the default logic and panel name (BBSIM `pdp11_cpumod.c:1103-1107`).

### 4.2 Hooks in SimH (BBSIM; the PI copy is identical)

| Where | What it records or does |
|---|---|
| `scp.c:2216-2238` | Globals: `realcons_memory_address_phys_register`, `realcons_memory_address_virt_register`, `realcons_memory_data_register`, `realcons_memory_write_access`, `realcons_memory_status` (SCPE_OK/NXM), `realcons_register_name`, `realcons_console_halt`. Event pointers: `run_start`, `operator_halt`, `step_start`, `step_halt`, `operator_exam`, `operator_deposit`, `operator_reg_exam`, `operator_reg_deposit`, `cpu_reset` |
| `scp.c:7214-7222, 7295-7303, 7313-7321` | RUN/GO, BOOT, CONT: refused with "Can not start: real console HALT or OFF" while `realcons_console_halt` is set. Otherwise fire `run_start` |
| `scp.c:7244-7246` | STEP fires `step_start` (no HALT check) |
| `scp.c:8095-8102` (`ex_addr`) | After a successful EXAMINE: phys := addr (**the address as typed**, even with `-v`), data, write=0, status. Fires `operator_exam` |
| `scp.c:8177-8181` (`get_aval`) | status := OK, or the error (e.g. SCPE_NXM) |
| `scp.c:8239-8247` (`dep_addr`) | After every DEPOSIT: phys, data, write=1, status. Fires `operator_deposit` |
| `scp.c:7849-7854`, `7975-7980` | ex_reg / dep_reg: register name and value. Fires `reg_exam` / `reg_deposit` |
| `scp.c:8349-8563` | `read_line_p()` replacement. The `sim>` readline runs in a pthread. The main thread loops with `sim_os_ms_sleep(1)` + `realcons_service()` (8543). Panel-generated command strings are **injected into stdin with `ioctl(0,TIOCSTI)`** (8472-8496, 8554) |
| `pdp11_cpu.c:743-783` | Macros `REALCONS_CPU_PDP11_MEMACCESS_*` in every CPU memory path: ReadE (2827, 2842), ReadW (2861), ReadB (2877), ReadCW (2900), ReadMW (2918), ReadMB (2932), PReadW (2944, 2957), PReadB (2968, 2981), WriteW (3010), WriteB (3025), WriteCW (3047), PWriteW (3056, 3070), PWriteB (3079, 3093). They record: `bus_ID_mode` = VA bit 16 (SimH's D-space flag), phys addr, virt addr (if a VA is known), data, write flag. **DMA (Map_Read/Write) is not hooked** |
| `pdp11_cpu.c:795-822` | `realcons_reloc(va)`: relocC with the mode taken from bits 18:17 and D space from bit 16. Used for PC → PA |
| `pdp11_cpu.c:950-952` | `realcons_service(cpu_realcons, 1)` at the top of every `sim_instr` loop pass. This includes the WAIT/idle loop, which `continue`s to the top (1066-1069) |
| `pdp11_cpu.c:1121-1123` | `saved_PC = PC` after the fetch |
| `pdp11_cpu.c:1152-1154` / `1174-1177` | WAIT fires `opcode_wait`. RESET fires `opcode_reset` |
| `pdp11_cpu.c:2564-2597` | After **every** instruction: `realcons_DATAPATH_shifter = dst` **only** for MOV, MOVB, ASL(B), ASR(B), ASH, ASHC ("ad hoc, so the known idle patterns appear right; tested for RSX11M, 2.11BSD, IAS"). Then copies IR and PSW. **If `realcons_console_halt` then `reason = SCPE_STOP`**: this is how the HALT switch stops the CPU, with no WRU or ^E involved. Otherwise fires `opcode_any` |
| `pdp11_cpu.c:2610-2631` | On exit from sim_instr: SCPE_STOP fires `operator_halt`. STOP_HALT, WAIT, VECABORT or SPABORT fire `opcode_halt`. Anything else (e.g. step count done) fires `step_halt` |
| `pdp11_cpu.c:3650-3657` | `cpu_reset()` fires `cpu_reset`. REALCONS then sets PC := the LOAD ADRS address (RC:486-494) |

### 4.3 REALCONS 11/70 console logic (RC, BB tree line numbers)

**State it keeps:**
- `loaded_address_15_00_datapathSR`: the CPU "SR", i.e. the console address bits 15:0.
- `loaded_address_datapathSWR`: SWR 21:16, latched by LOAD ADRS.
- `autoinc_addr_action_switch`: the last EXAM or DEP, used for stepping.
- `last_memory_status` and `run_state` (RC:109-124).

**Service cycle** (RC:756-1146), run at most every `interval` ms:
1. **POWER**: when the key is 0, halt. A 1→0 transition queues `quit`. The PI variant instead reads the command from `/dev/shm/pidp11/tmpsimhcommand.txt` (PI RC:774-795).
2. **Read inputs** (RC:784-811):
   - `panel_lock = PANEL_LOCK`.
   - `user_mode = (cm==USER)`.
   - **console_mode = !sim_is_running || console_halt** (RC:797).
   - Copy all 22 SR bits into SimH `SR` (RC:805). SimH's `SR` is an `int32` that programs read unmasked; whether bits 21:16 leak into program reads is UNVERIFIED.
   - `console_halt = HALT` (0 when locked).
3. **Action switch**: one per cycle, picked by rising edge between two polls, in priority order LOAD ADRS > EXAM > DEP > CONT > START. START reacts to **both** edges (RC:813-834). Actions are ignored unless in console mode (RC:845-846).
4. **ADRS ERR** (RC:857-868): when status becomes NXM in console mode, clear SR<15:0> (the LCM effect) and break the step sequence.
5. **Stepping** (RC:870-889): if the same action switch as last time, then `increment_address()` (RC:142-156): +1 in 17777700-717 with wrap to 700, error at 17777676 (then ADRS ERR and reload SR from the switches, as in LCM script 9), otherwise +2. Only bits 15:0 are stored, which gives the wrap within 64 KB. A different switch ends the sequence.

**Command generation** (written into `simh_cmd_buffer` and then typed into `sim>`):

| Panel | Condition | SimH command | Lines |
|---|---|---|---|
| LOAD ADRS | halted | none. Sets SR = SW<15:0>, SWR = SW<21:0>, shifter = 0, status = OK | 891-906 |
| EXAM | halted, no step error | `examine <spec>` | 909-920 |
| DEP | halted | `deposit <spec> <SW15:0 octal>` | 923-935 |
| START down | HALT=1 | `reset all`. The `cpu_reset` event then sets PC := LOAD ADRS | 949-953 |
| START up (release) | HALT=0 | `run <SR<15:0> octal>` | 959-967 |
| CONT | HALT=1 | `step 1`. **S BUS CYCLE is ignored**: there is no bus-cycle step | 968-970 |
| CONT | HALT=0 | `cont` | 971-973 |
| POWER 1→0 | | `quit` (BB) / contents of the /dev/shm file (PI) | 766-777 |

**`<spec>`** (RC:164-235):
- VIRTUAL positions: `-v -u|-s|-k [-d] -o <addr&0177777>`.
- Physical positions: `<addr & ~1>`, except 17777700-17777717, which map to register names R00..R05, KSP, PC, R10..R15, SSP, USP.

**Lamp computation**, every service cycle (RC:990-1143):
- **ADDRESS**:
  - In console mode, VA := SR (990-996).
  - CONS PHY: running shows `virt & 0xffff | SWR<21:16>`; halted shows `SWR<21:16> | SR<15:0>` (1017-1034).
  - PROG PHY: `phys` (1035-1038).
  - VIRTUAL: `virt & 0xffff` (1039-1044).
- **DATA**:
  - µADRS: a fake cycling pattern. While running, the 5-entry table {0x3bf, 0x32a, 0x3bd, 0x32c, 0x3f5}, labelled "from LCM2018 script Unixv7 idle". Halted: {0170, 0167} (FLOWS 14 COM.00). The index is `service_cycle_count % len` (95-107, 1055-1066).
  - DISPLAY REGISTER: `DR`.
  - DATA PATHS: the `shifter` variable.
  - BUS REG: SR switches when halted, otherwise the last memory data (1067-1081).
- **Parity**: cleared on a write. On a read below 017760000, computed by odd parity of the data. Unchanged on I/O-page reads (1085-1093).
- **MMR0_MODE** from `cm` (1098-1108).
- **ADDRESSING** from MMR0<0> and MMR3<4> (1114-1125).
- **DATA_SPACE** = VA bit 16 of the last access (1128).
- **MASTER** = console_mode (1131-1134).
- **RUN** = `sim_is_running` (1138-1143).
- **PAUSE** always 0; **PAR_ERR** never set.
- **ADRS_ERR** = (status == NXM) (999-1005).

**Event handlers:**
- `opcode_any`: addr = PC (relocated), PAUSE = 0 (301-316).
- HALT, RESET and WAIT opcodes: addr = PC, shifter = R0. RESET also busy-waits 10 ms while keeping the panel serviced (318-356).
- Operator halt and step halt: addr = PC, SR := PC (380-405).
- exam/deposit: in CONS PHY or PROG PHY, copy the address into SR and SWR; shifter = data (415-430).

### 4.4 Deviations and bugs in REALCONS (important if behavior is re-implemented)

1. **D-space console access is wrong.** VERIFIED (exp), section 8.1. REALCONS emits `-d` for the D positions (RC:176, 184, 192, with its own TODO at RC:162-163). In SimH `-d` means *decimal radix*. The PDP-11 D-space switch is **`-t`**: `relocC` tests `SWMASK('T')` (BBSIM `pdp11_cpu.c:3323`, OS `PDP11/pdp11_cpu.c:3079`), and the OS `doc/pdp11_doc.doc` says "-t if mem mgt enabled, force data space". So "USER D" and the other D positions examine and deposit **I space**.
2. **General registers only work in CONS PHY and PROG PHY.** In VIRTUAL positions 17777700 becomes VA 177700, which SimH reports as NXM. On real hardware GR access ignores the knob and shows the forced 21:06-on pattern.
3. Not implemented at all: S BUS CYCLE, PAUSE, PAR ERR.
4. ADRS ERR only reflects NXM on console exam and deposit. Odd-address, MMU (SCPE_REL) and program-time errors never light it.
5. **Command injection uses TIOCSTI.** It fails on this host: `/proc/sys/dev/tty/legacy_tiocsti = 0` and the ioctl returns EIO (VERIFIED (exp), section 8.2). It also needs stdin to be a TTY. REALCONS as shipped would therefore lose every panel action in a container or on modern kernels.
6. **Switches are polled.** Edges are detected only between consecutive polls (`blinkenlight_api_client.c:323-331`; with interval 8-20 ms). A press shorter than one poll interval is lost, and only one action is handled per poll.
7. PROG PHY after a virtual EXAM shows the **virtual** address. INFERRED: `ex_addr` records the address as typed (`scp.c:8097`), and `cpu_ex` reads memory directly, bypassing the hooked access routines.
8. DATA PATHS is only updated for MOV/ASx/ASH(C) results, by design, to make the idle patterns clean.
9. With SimH idling (`set cpu idle`), `realcons_service` only runs once per 100 passes of the wait loop (prescaler, `realcons.h:58`). BB's 11/70 scripts therefore use `set nothrottle` / `set noidle`, noting "under SimH 4, throttling results in a disturbed idle pattern" (`02.0_simh.machines/pdp1170-rsx-11m+_v4.6_mark/1170rsx11m.simh_pdp11:5-9`).
10. SimH itself stores a byte write to 177571 as `DR=byte` without shifting it into the high byte (VERIFIED (exp), section 8.1). This is a SimH quirk, not REALCONS.

### 4.5 Sampling, low-pass and brightness

**Sampling (SimH side).** There is no duty-cycle accumulation in SimH. Each service cycle *point-samples* the panel state left by the last bus cycle or instruction, then sends it by RPC; outputs are only sent when they changed (`realcons.c:430-509`).
- Default interval is 20 ms (`realcons.h:60`).
- PiDP uses `interval=8` (`systems/idled/boot.ini:4`). BB 11/70 scripts use `interval=1` (`pdp1170-rsx-11m+_v4.6_mark/1170rsx11m.simh_pdp11:44`, ias, blinky); rt11-games uses 5.
- The time check runs every 100 `sim_instr` loop passes (`REALCONS_SERVICE_HIGHSPEED_PRESCALE`, `realcons.h:58`).

**Low-pass (server side).** Each output bit keeps a ring buffer of (value, t_begin, t_end) entries, up to 256 (`blinkenlight_panels.h:60`). `historybuffer_get_average_vals(1e6/fmax)` computes a **time-weighted box-car average** over the last 1/fmax seconds, giving 0..255 per bit (PI `src/07.0_blinkenlight_api/historybuffer.c:218-366`). PISRV uses fmax = 10 Hz, so a **100 ms window** (`main.c:563-564`, comment "lamps are slow light bulbs ... Joerg's setting"). JAVA uses the same 100 ms (`Panel1170.java:413`).

**Resolution.** BB documents its own pipeline as "SimH interval 1 ms, LED lowpass 100 ms, updater 20 ms" (`07.1_blinkenlight_server/main.c:81-120`). Resolution is interval / window: 1 % at 1 ms, about 8 % at the PiDP's 8 ms.

**PiDP-11 PWM** (PISRV):
- The update thread runs every **50 ms** (`gpiopattern.h:34`).
- Brightness = avg x 32 / 256 gives **32 levels** (`gpiopattern.c:539-540`). These go through a "logarithmic" lookup table of 32x31 phases. Its highest level is a 28/31 (90 %) duty cycle (`gpiopattern.c:216-250`).
- Double buffering: the buffers are swapped after every update (552-553).
- Mux (`gpio.c:166-248`): 31 phases. Per phase, each of the 6 LED rows (x 12 columns) is lit for `intervl` = 50 µs (`gpio.c:68`) plus a 10 µs gap, then 3 switch rows are scanned. One PWM cycle is therefore at least about 31 x 6 x 60 µs ≈ 11 ms (≈ 90 Hz). The thread runs at SCHED_FIFO priority 98.
- A local LAMP TEST forces all ones (`gpiopattern.c:281-282, 445-448`).

**Java panel.** 16 brightness levels, drawn as alpha 0.1..1.0 (`DimmableLedControlSliceVisualization.java:41, 87`). Repaint every 50 ms (`Panelsim1170_app.java:89`).

**Rotary encoders** (PISRV `gpio.c:263-325`). Gray code sequence 11→01→00→10→11 means up, the reverse means down. `knobValue` is wrapped with `&7` / `&3`.

**open-simh alternative.** The `sim_frontpanel` API (`OS sim_frontpanel.h:125-262`) supports `sim_panel_add_register_bits()` with `sim_panel_set_sampling_parameters_ex(sample_frequency in instructions, dither ≤25 %, sample_depth)`. These are rolling per-bit averages computed **inside** SimH, for SimH REGs only. Bus address and data-path signals would need new CPU REGs or hooks.

### 4.6 PiDP-11-specific glue (VERIFIED (src))

- The SimH binary (`client11`) is BB SimH 4.0 + REALCONS. It is started by `bin/pidp11.sh` with `/dev/shm/pidp11/tmpsimhcommand.txt`, which contains `cd systems/<sel>; do boot.ini`, after the server (`server11`) has started.
- Rebooting via the knob is described in 2.5.
- On a POWER press, PI's REALCONS returns early in every service cycle while the knob is held (PI RC:774-797), so the lamps freeze.

---

## 5. Licenses

| Item | License | Evidence | Implication for us |
|---|---|---|---|
| BlinkenBone repo (REALCONS, API, servers, Java panel, **including the 11/70 photo-based PNGs and coordinate CSVs**) | **MIT**, © 2016 Joerg Hoppe | `BlinkenBone/LICENSE`; per-file MIT-style headers (e.g. RC:1-21); GitHub API `license: mit` | Copying is allowed with the copyright and permission notice included. Re-implementing the behavior is cleanest. The panel photos are photos of a DEC product: copyright is MIT via the repo, but treat DEC trade dress with care |
| pidp11 repo as a whole | **No license file.** GitHub API `license: null` | `pidp11/` has no LICENSE | Default copyright. Do not copy files that lack a header |
| pidp11: BB-derived sources (REALCONS, `src/07.*`, `11_pidp_server/pidp11/*.c`) | MIT-style (Joerg Hoppe; gpio.c "Oscar Vermeulen & Joerg Hoppe") | file headers | Copying allowed with the notice |
| SimH files (both trees) | SimH X11-style (Robert M Supnik et al.) | file headers | Permissive |
| Mike Hill's IDLED (`systems/idled/boot.ini`) and BLINKY (BB `pdp1170-blinky`) | **"Copyright (c) Mike Hill 2018/2017 All Rights Reserved"** | file headers | **Do not redistribute or bundle** without permission. Write our own demo |
| `bin/test.py` (Neil Higgins) | "Free as (in beer) and free (as in thought)" | header | Informal |
| `src/sty` (st terminal) | "See LICENSE file" but the file is missing. Upstream suckless st is MIT (UNVERIFIED for this fork) | `src/sty/config.h:1` | n/a |
| `pinctrl` (in PISRV) | No header. Upstream Raspberry Pi utils is BSD-3 (UNVERIFIED) | | n/a |
| PiDP-11 manual | No license stated | PIMAN | Reference only |
| DEC manuals and schematics (bitsavers scans) | DEC copyright | | Reference only. Quote briefly; do not redistribute |

---

## 6. Recommendations for our re-implementation (my synthesis, not verified behavior)

1. **Drive SimH through an API or in-process hook, not TIOCSTI.** Options: a REALCONS-like in-process module, or open-simh's remote console or `sim_frontpanel` interface.
2. **Use `-t` for D space** in examine and deposit.
3. Map GR addresses to SimH register names in **every** knob position, and render the GR display pattern from Table 2-1.
4. Make switch input **event-driven** from the browser (press and release messages, queued) so short clicks are never lost. Enforce:
   - "halted only" for LOAD ADRS, EXAM, DEP, CONT and START;
   - LOCK semantics;
   - the EXAM/DEP coupling and step rules (+2 / +1 / wrap / 676 boundary / 64 KB block / reset on LOAD ADRS and on errors).
5. **Brightness.** Accumulate per-bit ON time inside the simulator, weighted by wall-clock or simulated time, *including time spent in WAIT*. Alternatively sample at ≥ 1 kHz.
   - Average over a **100 ms** box-car or an exponential low-pass with τ ≈ 50-100 ms, matching BB and PiDP.
   - Send frames to the browser at 30-60 Hz with about 32 levels and a gamma curve similar to PiDP's log table.
   - The idle patterns then appear naturally, provided DATA PATHS shows **R0 during WAIT**.
6. Implement improvements that REALCONS leaves out:
   - ADRS ERR pulses on odd, NXM or MMU traps while running;
   - MASTER/PAUSE flicker from I/O-page or cache-miss proxies;
   - parity lamps off for I/O-page cycles.
   - S BUS CYCLE as "stop after the next memory access". This needs a hook in the CPU memory routines, and the console must be disabled while stopped mid-instruction.
7. Light the knob-position lamps and LAMP TEST purely in the UI, as the real hardware does.

## 7. Open questions / UNVERIFIED items

- Whether the real knobs have mechanical end stops. Replicas wrap, and SCH shows "C.W." plus a circular arrow.
- Whether LAMP TEST is momentary (DEC says "when raised"; the replicas use momentary).
- Real-hardware EXAM on an odd address, and whether parity is odd or even.
- The initial value of the RSX `$IDLPT`, and the RT-11 `SR_` symbol value.
- The exact DATA PATHS content after a HALT-switch stop or an S INST step (flows-dependent).
- How DEP and EXAM of the DR behave on byte writes on real hardware.

---

## 8. Experiment transcripts

Everything was run in `r2-realcons/work`. OS was built with `make -j16 pdp11 TESTS=0 NOVIDEO=1` (19 s, exit 0, binary `dl-opensimh/simh/BIN/pdp11`).

### 8.1 SimH: D-space switch, GR addresses, SR/DR (`scripts/t1_dspace.ini`, `t2_dspace.ini`, `t3_srdr.ini`)

Setup: KIPAR0=0 and KDPAR0=0200 (D page 0 → PA 020000), MMR3=4, MMR0=1, PA 0 = 111111, PA 20000 = 122222.

```
=== ex -v -k 0 (expect I-space 111111)
0:	111111
=== ex -v -k -t 0 (D-space if -t is the D switch -> 122222)
0:	122222
=== ex -v -k -d 0 (REALCONS uses -d)
0:	37449                 <- -d = decimal radix (111111 octal = 37449)
=== ex -v -k -d -o 0 (REALCONS generated form)
0:	111111                <- I space, not D space
=== general register addresses in I/O page
ex 17777700  -> Address space exceeded
ex 17777707  -> Address space exceeded
=== registers by name
R00: 000000  R10: 000000  KSP: 000000  SSP: 000000  USP: 000000  PC: 000100
ex SYSTEM SR -> SR: 000000 ; dep SYSTEM SR 123456 ; ex 17777570 -> 17777570: 123456
dep 17777570 54321 ; ex SYSTEM DR -> DR: 054321
ex 1 -> 1: 111111 (bit 0 ignored) ; ex 7777776 / ex 17760000 -> Address space exceeded (NXM)
```

`t3_srdr.ini` ran this program at 1000: `MOV @#177570,R0; MOV R0,@#177570; MOVB #252,@#177571; HALT`, with SYSTEM SR = 123456:

```
HALT instruction, PC: 001020 (HALT)
R0:	123456
DR:	000252          <- byte write to 177571 stored unshifted
PC:	001020          <- address after HALT
```

### 8.2 TIOCSTI (the REALCONS command-injection technique) on this host

```
$ cat /proc/sys/dev/tty/legacy_tiocsti ; uname -r
0
7.0.0-38-generic
$ script -qec ./tiocsti_test /dev/null
isatty(0)=1
ioctl(TIOCSTI) failed: Input/output error (errno=5)
```

### 8.3 Idle pattern sequences (`scripts/idle_patterns.py`, `python3 -I`)

This script is my re-implementation of the published instruction sequences. `*` = lamp on, bit 15 on the left.

```
RSX-11M (start 170017 as in IDLED)        2.11BSD (start 0377)        IAS (start 1)
 0 ****........****                        ........********            ...............*
 1 .****......****.                        .......********.            ..............**
 2 ..****....****..                        ......********..            ... fills leftwards to
 3 ...****..****...                        ...                         **************** (15)
 4 ....********....                        ********.......* (8)        then empties from the
 5 *....******....*                        *******.......** (9)        right to ................ (31)
 6 **....****....**                        ...                         and repeats
 7 ***....**....***
 8 ****........****  (period 8 steps)
```

### 8.4 Schematic and photo reads

- `pdftoppm -r 200 -f 5 -l 5 1170_ctlPanelSchem.pdf` followed by an ffmpeg crop (`work/schem5_tables.png`, `schem5_s32.png`, `schem5_s33.png`) gave the code tables in 2.2 and 2.3 and the paired S33 contacts.
- ffmpeg crops of the BB background photo (`work/knobs_crop.png`, `switches_lamptest.png`, `top_left.png`) gave the label grids, the white LAMP TEST switch, the indicator labels, and the key-switch positions.

## 9. Coordinate tables found (for the visual researcher)

- BB Java 11/70 panel: `09_javapanelsim/resources/blinkenbone/panelsim/panelsim1170/images/pdp1170_size=<W>_coordinates.csv`, with W in {800, 1000, 1200, 1340, 1580, 1850, 2400, 2950, 5424}. Format: `filename;left;top`. About 115 entries of per-control overlay PNGs on a photo background; 5424 is the master Photoshop scale. MIT.
- For example, at W=1200 the background is 1200x688: ADDRESS knob at (1037,229), DATA knob at (1032,358), LAMP TEST at (837,506), status lamps at y=211 from x=441 (PAR ERR) to x=819 (22), PARITY HIGH/LOW at (202,400)/(236,399).
- PiDP-11 has no graphics, only the GPIO matrix: LED rows 0-5 by 12 columns, switch rows 0-2 (`main.c:594-665`).

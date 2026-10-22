# Research reports (2026-10-21)

Background research done before the design was fixed. Their paths beginning
with `$SCRATCH` point into the research session's scratch directory, an
ephemeral working directory that no longer exists; the findings themselves are
what matter.

* `r1-simh-control.md`: how to drive and observe Open SIMH's PDP-11 from outside;
  why remote-console master mode / the frontpanel API were rejected; the panel patch
  prototype and its measured cost.
* `r2-panel-semantics.md`: exact behaviour of the PDP-11/70 console (lamps, switches,
  knobs, EXAM/DEP stepping), how REALCONS/PiDP-11 do it, OS idle patterns.
* `r3-os-images.md`: OS images, download URLs/sha256, boot automation, licences.
* `r4-visual-spec.md`: geometry, colours, typography and interaction design of the
  panel replica; xterm.js and font choices.

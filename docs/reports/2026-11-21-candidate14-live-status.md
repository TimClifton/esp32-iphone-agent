# Candidate 14 generic phone acceptance — incomplete

Source branch: `lab/windows-ble-pointer` in `TimClifton/esp32-iphone-agent`. Decoder correction `c9c2d20` is pushed. Runtime is `0.1.4-local-pointer14`, SHA-256 `EA90853BECD7587BC0C7FB0CCA3B513487CAD77DB345864AC7B157A893C9C33F`.

## Current evidence

- The owner confirmed current readiness: native USB connected to iPhone, unlocked, home WiFi, Screen Mirroring active. Desktop control uses WiFi after Bluetooth authentication; pointer status remains BLE. USB reports connected. These are separate links.
- Decoder regression tests now require correct timestamps for every synthetic marker, including filler-only and pre-IDR cases. Independent review found no P1/P2 for the tested non-reordering stream.
- Cold replay reached a current image with 1,677 picture AUs / 1,677 BMPs / zero pending timestamps. Later live checks reached 4,506/4,506 and 5,167/5,167, still zero pending and zero reassembly gaps. Old frames remain rejected. This does not establish the exact cause of the previous live 137-frame deficit.
- Current-runtime calibration passed at 0.218% error. A targeted Search tap opened the expected field. Exact `CP test 42` appeared; Cmd+A then `Ready14` replaced it exactly. Luna independently checked the neutral after-images. No search was submitted.
- Native Home changed the display to the app switcher, not Home. Terra independently confirmed this. Escape and a separate Cmd+H did not produce Home. A precise tap on empty app-switcher background dismissed it back to Search, verified by a fresh image.
- Floating AssistiveTouch targeting remains unaccepted: the guard refused one missing-pointer attempt and another with a 4.04% residual. No click was sent for those failures. A later top-background tap stopped on a freshness error. Acknowledgements are not destination verification.
- The owner has been asked to set the actual pointer colour to Red under Accessibility > Pointer Control, leaving other settings unchanged. That change is not yet confirmed.

## Remaining finite acceptance

### Supervised continuation

The owner set Red, enabled Increase Contrast and disabled Automatically Hide Pointer. A fresh screenshot verified those values; pointer size remained at its prior setting. Calibration then passed at 0.149%. During an explicitly untouched window, targeting the floating AssistiveTouch control opened its menu; targeting its visible Home control reached the actual Home screen. Luna independently verified that after-image. This is a verified visual Home route, not a repair of the native Home shortcut.

From Home, a targeted Search tap and USB text entry produced exact `Settings`; selecting the observed Settings result opened the expected Accessibility page. Wheel deltas 3 and 10 produced only small movement and are not accepted as useful scrolling. A bounded drag from (350,500) to (350,1150) visibly scrolled to earlier Accessibility sections. No business application controls were used.

Desktop stream restart then failed recovery: its request was acknowledged, but board status reported `fz=1`, GOP 4188261, overflow 1, generation 4 and zero published pictures. The subsequent pointer refresh refused input with an explicit phone-side Screen Mirroring restart instruction. Decoder timestamp correction does not repair this board replay/keyframe limitation. Do not advertise automatic recovery or repeatable unattended startup. Recovery remains a release limitation requiring a manual phone-side restart, and generic hardened promotion remains incomplete.

The supervised console was released with an empty line and exited 0. Its controller process was absent on the subsequent check. Pairing and settings were preserved; no capture loop or phone controller remains active.

Source commit `ec94528` additionally corrects Home reporting: a changed screen returns an unverified-destination error and stops fallbacks; pointer regressions and vet passed. It is pushed but has not been rebuilt into candidate14 or live-tested. Do not confuse the active candidate14 results with that newer source change.

Verify reliable Home via an observed route, targeted navigation with the contrasting pointer, visible bounded wheel scrolling, current-runtime stream recovery, and clean release. Do not add an application-specific journey as a gate. Do not claim generic acceptance, CP migration, CP installation or an upstream merge before those results exist.

The small model-tier check is in `2026-11-20-phone-tool-tier-check.md`: deterministic mechanics require no model; Luna passed simple state/text checks and Terra passed visual interpretation. No billing evidence establishes an absolute cheapest model.

Private phone images, credentials and runtime profiles are excluded from source. Preserve the upstream MIT LICENSE, NOTICE and Henry Yuan attribution. Legacy Docker iOS emulation is a separate experiment and is excluded from the proposed CP tool.

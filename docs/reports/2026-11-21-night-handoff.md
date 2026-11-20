# Real-iPhone tool: night handoff

The phone controller was released cleanly at the owner's request. No phone input, capture polling or viewer remains active. Pairing is preserved. The board may be disconnected from the phone. Do not resume physical testing without phone availability and a current verified screen.

## Delivery state

Source is pushed to `TimClifton/esp32-iphone-agent`, branch `lab/windows-ble-pointer`. No third-party upstream merge or CP repository migration has occurred. Only read-only CP placement/instruction checks were performed. The condition for CP migration (generic process working and hardened) is not yet met. Turnkey and other app journeys are downstream work, not this tool's acceptance gate.

Latest built engine is candidate 13 from `4911cb8f6043720da25d7c3672fc683cda77c2eb`, version `0.1.4-local-pointer13`, SHA-256 `7EBBF6CB0FCD89242C869E50F32334CBA0F39F625A99A1C8DB1CA34A76EF0B10`. All 25 Python helper tests passed; affected Go tests passed. Candidate 13's wheel primitive has not passed live UI validation because cold replay failed the image freshness guard.

## What is proven and what is not

- The board is an ESP32-S3-N16R8 running dual firmware 0.2.7; the phone is the owner's iPhone 16 Pro Max. Native USB is now connected to the phone, not the PC. Pointer mode remains BLE, while desktop video uses WiFi after Bluetooth authentication.
- USB-to-phone typing visibly produced exact `CPTest42` and `AssistiveTouch` in neutral Search fields. Bluetooth-only typing had previously been acknowledged without visible text. This proves an improvement for typing, not a general USB-versus-Bluetooth benchmark. Repeat text verification on the final corrected runtime.
- Precise Search taps and multiple small Back-button targets were verified with fresh images. Calibration passed on several candidates, including errors below 0.5%. This does not certify every target or gesture.
- Candidate 12's explicit one-unit pointer refresh repeatedly produced a verified new frame on still Settings pages. Guarding requires a recent joint connected BLE pointer report, same session/generation, serialized operation and a post-ACK frame; it does not relax image age limits. Dwell Control was visually observed off.
- Desktop stream restart recovered once after roughly 35 seconds, physically confirmed by the owner. It is not an iPhone AirPlay restart and does not recover every frozen/aged stream.
- Native Home remains unaccepted and has reached the app switcher. AssistiveTouch-menu Home is still pending because targeting the grey floating button was refused; pointer contrast work is unfinished. The owner allowed colour/size changes, but no colour or size change has yet been confirmed.
- Drag acknowledgements did not consistently move the Accessibility list. The added bounded wheel input still needs live verification. An ACK must never be called UI success without the expected after-image.
- Strict lease/process ownership and clean release worked. A separate agent could not access the root chat's console ID, then successfully opened and released its own supervised session. Its frozen-image condition correctly prevented input.
- Windows GATT setup remains intermittent. One bounded setup retry with fresh-client cleanup and one bounded readiness retry are implemented and fixture-tested; not all cold starts have passed final live acceptance.
- Long mirror sessions can fill the board's GOP. A new controller may then get `fz=1`; desktop reconnect cannot create a new iPhone keyframe, so fresh phone-side mirroring may be required. BLE-only controller transport receives video, but its compact status omits `hidm`, preventing the currently required joint-mode refresh proof. Do not bypass that guard.

## Reproduced decoder defect

Live diagnostics showed a constant deficit of 137: input/output advanced from 5380/5243 to 5749/5612, with zero input queue and wire gaps, while assigned output times lagged about 30 seconds. No new image was accepted from that replay; no blind phone action followed.

An entirely synthetic marker fixture reproduced two permanent FIFO-skew mechanisms with the current FFmpeg flags:

| Input case | Count after normal EOF | Independent marker result |
| --- | --- | --- |
| Clean low-bitrate GOP | 192 pictures in / 192 BMPs / 0 pending | All timestamps matched. |
| Three valid filler-only AUs | 195 assumed pictures / 192 BMPs / 3 pending | Skew begins at marker 40; last marker191 labeled38600 instead of39200. |
| CONFIG then three pre-IDR P pictures | 195 assumed pictures / 192 BMPs / 3 pending | Skew begins at marker0; same final timestamp error. |

The exact cause of the live 137 deficit has not been measured from its payloads. The fixture establishes two real source defects, not a claim that either alone explains all live failures. The opt-in diagnostic is `cli/internal/video/decoder_replay_fixture_test.go`; run with `go test -tags replaydiagnostic ./internal/video -run TestDecoderReplayMarkerDistinguishesBufferedAndMissingPictures -v` using the bundled FFmpeg. It records current bad behaviour and must become correct-timestamp regression coverage with the fix.

Smallest supported next correction: keep a new/gapped reassembler gated on its first IDR even after CONFIG, and enqueue timestamps only for Annex-B AUs containing picture VCL data while still feeding parameter sets/metadata. Never shorten the timestamp FIFO or relabel old pictures as new. Preserve strict generation, post-action, age, status and ownership checks. No decoder fix or new runtime build was made after this diagnosis tonight.

## Next bounded acceptance

Implement that correction and run its focused synthetic regressions, build once, then perform a short generic session: startup/current image, idle refresh, targeted tap, visible wheel scroll, exact text replacement, Home by a verified route, stream recovery and clean release. Stop on an unverified result. Do not begin app-specific journeys or claim CP installation/merge until those steps and the eventual CP package checks actually pass.

Apple's dedicated iOS26 pointer guide places colour at Settings > Accessibility > Pointer Control > Color, despite its generic AssistiveTouch guide still mentioning Pointer Style. That is the intended contrast route if needed; inspect the actual visible page before selecting it.

The small Luna/Terra evaluation is preserved in `2026-11-20-phone-tool-tier-check.md`: fixed mechanics are agent-free scripts, Luna passed simple screen/text checks, Terra passed visual target proposals. Provider billing and exact runtime model IDs were not exposed, so no absolute-cheapest or monetary-savings claim is justified.

Private screenshots and runtime state are excluded from Git. A metadata-only check found zero plaintext pairing-key fields in the active profile; authenticated runtime use loads the protected Windows credentials. No paid hosted Manina inference or paid API was configured. Retain Henry Yuan's ESP32 iPhone Agent MIT LICENSE/NOTICE and attribution in any eventual CP derivative. Legacy Linux emulation is separate and must not be migrated as this tool.

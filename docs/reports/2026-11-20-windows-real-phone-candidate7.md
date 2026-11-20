# Windows real-iPhone candidate 7 live result

Source branch: `lab/windows-ble-pointer` in the user-owned `TimClifton/esp32-iphone-agent` fork. No upstream PR, upstream merge or CP repository migration occurred.

Built source revision `6875a331159fa3f667449f9a7c6edd19f23c1bc2`, version `0.1.4-local-pointer7`, SHA-256 `E1A91A0CD1E1A30EE166AAE8A854A1C4E91EE159039AF771F072D5FCC13AE056`. Focused pointer/daemon tests, vet and 21 Python tests passed; independent source review found no P1/P2 regression in the timing change. The correction requires fresh baseline, nudge and return samples after the acknowledged movement plus an 800 ms glide interval. The 0.8% accuracy tolerance is unchanged.

Actual hardware: ESP32-S3-N16R8 with dual firmware 0.2.7 and an iPhone 16 Pro Max reporting iOS 26.6.1. No hosted Manina inference or paid API was configured.

## Live evidence

- Candidate 6 produced a fresh Control Center capture independently confirmed by the owner. Home calibration passed at 0.68%, but precise targeting was refused, with no click sent.
- Candidate 7 initially encountered a Windows GATT setup timeout; a retry connected and handed the authenticated session to home WiFi.
- Incoming video/status were recent, but a long cached replay did not produce a usable decoded image. Restarting phone mirroring within the same owned session produced a fresh Home image immediately. The first-screen problem remains unresolved.
- On that fresh stream, candidate 7 calibration passed at 0.47% with span 560 by 1223. A verified tap on the Home-screen Search button returned success, and a fresh after-image showed Search open with the keyboard. This is a real precise-input acceptance, not just an acknowledgement.
- ASCII text `Settings` returned a transport acknowledgement, but the after-image did not show the text. Text entry is unverified/failed for this test.
- Home returned an explicit failure. Its fresh after-image showed the app switcher, not Home. A subsequent verified neutral tap succeeded and returned to Search. Home navigation is not accepted.
- The owned session exited cleanly; no continuous viewer or capture polling remains. Pairing is preserved. Private screenshots and runtime profiles are excluded from this report and Git.

## Remaining promotion conditions

Do not label this ready for general CP app tests or migrate it into CP repos yet. Resolve repeatable first-connect/capture behavior and the text/Home limitations; retest supported operations on the actual phone. A successful isolated Search tap does not establish a reliable general agent interface. Preserve upstream MIT credit and NOTICE in any eventual CP derivative.

The old Linux Docker emulator is a separate experiment and is not the CP tool proposed here.

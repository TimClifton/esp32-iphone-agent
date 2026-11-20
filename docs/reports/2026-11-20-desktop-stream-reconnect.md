# Real-phone desktop stream reconnect test

User requested a streaming reconnect test. Source `09ef2b64cc75fbeb3cf7896d66206e4dab1da4b9`, branch `lab/windows-ble-pointer`, is pushed to the user-owned `TimClifton/esp32-iphone-agent` fork. No third-party upstream or CP migration occurred.

Built candidate 9: `0.1.4-local-pointer9`, SHA-256 `3D9C82931B10B0268D89C90AC1EF39B917FAD7E92510993169FDD758C2A7E7EF`. Focused daemon/app tests, 21 Python tests and independent source review passed. A review finding about accepting a pre-restart image was fixed and regression-tested before the build.

## Actual result

The real ESP32 authenticated successfully. Its first screenshot failed because the decoder had not caught up. In the same supervised connection, `device restart-stream` successfully confirmed the board's CLI video cursor stopped, reset the desktop decoder/frame generation, and resumed one continuous replay. Pairing, pointer mode and phone-side AirPlay mirroring were preserved. No phone input was sent in this test.

Several screenshot retries still failed safely during replay. At approximately 35 seconds, a screenshot passed the post-restart capture-time barrier and showed the app switcher with the Settings Touch card. The owner independently confirmed that this matched the physical phone. No additional manual Screen Mirroring restart was used for this recovery.

This verifies one successful desktop-feed recovery, not instant or reliable recovery across all conditions. It does not prove that restarting was necessary instead of waiting longer on the original decoder. It does not stop/restart the iPhone's own Screen Mirroring session. The command's acknowledgement alone is not a recovery pass; the later physically matched image is the evidence.

The session was released afterward. Private phone images and pairing state remain local and are not included in Git.

## Outstanding conditions

Cold Windows GATT connection timeouts remain intermittent. Replay/capture startup remains slow. Candidate 7 verified precise Search tapping, but direct text entry was not visibly confirmed. Home still reached the app switcher with candidate 8's atomic flick and is not accepted. These limits prevent describing the tool as ready for routine CP app testing; CP migration remains conditional on further hardening and live acceptance.

Based on ESP32 iPhone Agent by Henry Yuan; retain its MIT LICENSE/NOTICE and credit in public demonstrations. No paid hosted Manina inference or API was configured.

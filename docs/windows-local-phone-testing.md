# Windows local phone-testing correction

Current source candidate: **6**. See `tools/windows-phone-testing/README.md` for the supervised Windows launchers, protected pairing storage, timeout diagnosis and the outstanding live acceptance gate. The older build example below records the earlier pointer correction; use version `0.1.4-local-pointer6` and output `esp32agent-local6.exe` for the current candidate.

This local branch changes only the CLI. It does not change the iPhone app, board firmware, accounts or hosted model service.

Observed on a physical iPhone 16 Pro Max with iOS 26.6.1 and ESP32-S3 dual firmware 0.2.7:

- Repeated calibration samples showed a consistent horizontal origin offset greater than 10%. The existing correction discarded that offset. Accept a larger offset only for separated observations fitting a near-unit mapping with low residual.
- The corrected origin can require negative movement relative to the firmware's home point. Clamping the corrected coordinate to zero prevented reaching a small left-edge Back button. Permit bounded movement beyond that origin.
- iOS animates pointer movement after the board acknowledges it. Wait for the final pointer correction to settle before clicking.
- On reconnect, a cached GOP may contain more than 30 pending pictures. Dropping the oldest queued timestamps can label an old decoded picture as new. Preserve FIFO timestamps.
- A screenshot must be recent and, after input, captured after that action. Return an explicit error when decoding has not caught up. Coordinate actions also require a current screen first.

Regression checks cover coherent and uncertain offsets, left-edge movement, settling before click, cached-GOP timestamp order and stale screenshot rejection. Tests and vet checks were run with FFmpeg 7.1; an older FFmpeg on PATH did not support the upstream decoder's fps_mode option.

Build using Go 1.26 or later:

```powershell
cd cli
go test ./internal/pointer ./internal/video
go test ./internal/daemon -run 'TestScreenshotRejectsCachedOldPicture|TestScreenshotAcceptsRecentlyCapturedPicture'
go vet ./internal/pointer ./internal/video ./internal/daemon
$env:CGO_ENABLED='0'
go build -ldflags '-X github.com/onehumanbeing/esp32-iphone-agent/cli/internal/app.Version=0.1.4-local-pointer3' -o esp32agent-local.exe ./cmd/esp32agent
```

The separate local Windows transport bridge uses Bleak with random-address handling for the actual physical board. It retains the official CLI's authentication, encryption and actions. The candidate sets the actual board discovery label and rejects a mismatching protocol identity before forwarding authentication responses. No simulated frames are used.

These are local fixes, not an upstream release or merge. No phone screenshots or pairing keys are included in this repository.

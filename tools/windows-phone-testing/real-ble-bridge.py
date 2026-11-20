"""Real board BLE transport for the official CLI's length-prefixed pipe adapter.

Uses no simulated board or screen. The upstream adapter labels discovery with a
fixed test name; authenticated board identity comes from the actual hardware.
"""
import asyncio
import struct
import sys
import time
from bleak import BleakClient, BleakScanner

BASE = '6e400001-a9e8-4b1b-9c39-6d6f8a3c'
STARTED=time.monotonic()
CONNECTED_CLIENT=None
REJECTED_CLIENT = None
REJECTION_DISCONNECT = None
STARTUP_TIMEOUT = 35
GATT_ATTEMPT_TIMEOUTS = (20, 12)
DISCONNECT_TIMEOUT = 3

def stage(message):
    print(f'{time.monotonic()-STARTED:.3f}s {message}',file=sys.stderr,flush=True)

def read_frame():
    header = sys.stdin.buffer.read(2)
    if len(header) != 2:
        return None
    size = struct.unpack('<H', header)[0]
    data = sys.stdin.buffer.read(size)
    return data if len(data) == size and size else None

def receive(_, data):
    global REJECTED_CLIENT, REJECTION_DISCONNECT
    client = CONNECTED_CLIENT
    if client is None or client is REJECTED_CLIENT:
        return
    board_id = None
    if data and data[0] == 0x81: board_id=bytes(data[33:39])
    if data and data[0] == 0x82: board_id=bytes(data[17:23])
    if board_id is not None and board_id.hex() != '2884854f6d05':
        REJECTED_CLIENT = client
        REJECTION_DISCONNECT = asyncio.create_task(client.disconnect())
        error=b'\xe0Unexpected board identity; connection refused'
        sys.stdout.buffer.write(struct.pack('<H',len(error))+error)
        sys.stdout.buffer.flush()
        return
    sys.stdout.buffer.write(struct.pack('<H', len(data)) + data)
    sys.stdout.buffer.flush()

async def disconnect(client):
    if client is REJECTED_CLIENT:
        await REJECTION_DISCONNECT
    else:
        await client.disconnect()

async def discard_client(client, deadline):
    global CONNECTED_CLIENT
    if CONNECTED_CLIENT is client:
        CONNECTED_CLIENT = None
    remaining = min(DISCONNECT_TIMEOUT, deadline - asyncio.get_running_loop().time())
    try:
        if remaining <= 0:
            return False
        async with asyncio.timeout(remaining):
            await disconnect(client)
        return True
    except Exception as exc:
        stage('Windows GATT cleanup failed: ' + type(exc).__name__)
        return False

async def connect_board():
    global CONNECTED_CLIENT
    loop = asyncio.get_running_loop()
    deadline = loop.time() + STARTUP_TIMEOUT
    async with asyncio.timeout_at(deadline):
        board = await BleakScanner.find_device_by_filter(
            lambda d, a: BASE + '0010' in a.service_uuids
            and (not (a.local_name or d.name) or (a.local_name or d.name).endswith('6D05')), timeout=8)
        if board is None:
            raise RuntimeError('Real ESP32 Agent 6D05 CLI advertisement not found')
        stage('Found real board; starting Windows GATT connection')
        for attempt, budget in enumerate(GATT_ATTEMPT_TIMEOUTS):
            remaining = min(budget, deadline - loop.time() - DISCONNECT_TIMEOUT)
            if remaining <= 0:
                raise TimeoutError('No time left for Windows GATT setup and cleanup')
            client = BleakClient(
                board, timeout=remaining, services=[BASE+'0010'],
                winrt={'use_cached_services': False, 'address_type': 'random'},
            )
            CONNECTED_CLIENT = client
            def notify(sender, data, expected=client):
                if CONNECTED_CLIENT is expected:
                    receive(sender, data)
            try:
                async with asyncio.timeout(remaining):
                    await client.connect()
                    stage('Connected real board')
                    await client.start_notify(BASE+'0012', notify)
                    if REJECTED_CLIENT is client:
                        raise RuntimeError('Unexpected board identity; connection refused')
                    stage('Subscribed real board')
                return client
            except (Exception, asyncio.CancelledError) as exc:
                cleaned = await discard_client(client, deadline)
                if REJECTED_CLIENT is client:
                    raise RuntimeError('Unexpected board identity; connection refused') from None
                if not (isinstance(exc, TimeoutError) and attempt == 0 and cleaned):
                    raise
                stage('Windows GATT setup timed out; retrying once with a fresh client')

async def main():
    client=await connect_board()
    try:
        while True:
            frame = await asyncio.to_thread(read_frame)
            if frame is None or client is REJECTED_CLIENT:
                break
            await client.write_gatt_char(BASE + '0011', frame, response=True)
    finally:
        await asyncio.wait_for(disconnect(client),5)

if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as exc:
        stage(type(exc).__name__ + ': ' + str(exc))
        sys.exit(1)

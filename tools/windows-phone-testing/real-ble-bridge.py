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
    board_id = None
    if data and data[0] == 0x81: board_id=bytes(data[33:39])
    if data and data[0] == 0x82: board_id=bytes(data[17:23])
    if board_id is not None and board_id.hex() != '2884854f6d05':
        error=b'\xe0Unexpected board identity; connection refused'
        sys.stdout.buffer.write(struct.pack('<H',len(error))+error)
        sys.stdout.buffer.flush()
        if CONNECTED_CLIENT is not None:
            asyncio.create_task(CONNECTED_CLIENT.disconnect())
        return
    sys.stdout.buffer.write(struct.pack('<H', len(data)) + data)
    sys.stdout.buffer.flush()

async def connect_board():
    global CONNECTED_CLIENT
    board = await BleakScanner.find_device_by_filter(
        lambda d, a: BASE + '0010' in a.service_uuids
        and (not (a.local_name or d.name) or (a.local_name or d.name).endswith('6D05')), timeout=8)
    if board is None:
        raise RuntimeError('Real ESP32 Agent 6D05 CLI advertisement not found')
    stage('Found real board; starting Windows GATT connection')
    client=BleakClient(board,timeout=20,services=[BASE+'0010'],winrt={'use_cached_services':False,'address_type':'random'})
    CONNECTED_CLIENT=client
    try:
        await client.connect()
        stage('Connected real board')
        await client.start_notify(BASE+'0012',receive)
        stage('Subscribed real board')
        return client
    except BaseException:
        try: await asyncio.wait_for(client.disconnect(),3)
        except BaseException: pass
        raise

async def main():
    async with asyncio.timeout(35):
        client=await connect_board()
    try:
        while True:
            frame = await asyncio.to_thread(read_frame)
            if frame is None:
                break
            await client.write_gatt_char(BASE + '0011', frame, response=True)
    finally:
        await asyncio.wait_for(client.disconnect(),5)

if __name__ == '__main__':
    try:
        asyncio.run(main())
    except Exception as exc:
        stage(type(exc).__name__ + ': ' + str(exc))
        sys.exit(1)

import asyncio
import importlib.util
import unittest
import io
from pathlib import Path
from unittest.mock import patch, AsyncMock

spec=importlib.util.spec_from_file_location('real_bridge',Path(__file__).with_name('real-ble-bridge.py'))
bridge=importlib.util.module_from_spec(spec)
spec.loader.exec_module(bridge)

class Scanner:
    @staticmethod
    async def find_device_by_filter(*args,**kwargs): return object()

class Client:
    last=None
    def __init__(self,device,**kwargs):
        Client.last=self
        self.options=kwargs
        self.disconnected=False
    async def connect(self): pass
    async def start_notify(self,*args): pass
    async def disconnect(self): self.disconnected=True

class StartupTests(unittest.IsolatedAsyncioTestCase):
    async def test_wrong_handshake_identity_is_rejected_without_waiting(self):
        output=io.BytesIO()
        client=type('Client',(),{'disconnect':AsyncMock()})()
        with patch.object(bridge,'CONNECTED_CLIENT',client),patch.object(bridge.sys,'stdout',type('Output',(),{'buffer':output})()):
            bridge.receive(None,b'\x82'+b'0'*16+b'wrong!'+b'0'*16)
            await asyncio.sleep(0)
        client.disconnect.assert_awaited_once()
        self.assertEqual(output.getvalue()[2],0xE0)
    async def test_eof_disconnects_and_uses_random_cli_service(self):
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',Client),patch.object(bridge,'read_frame',lambda:None):
            await bridge.main()
        self.assertTrue(Client.last.disconnected)
        self.assertEqual(Client.last.options['services'],[bridge.BASE+'0010'])
        self.assertEqual(Client.last.options['winrt']['address_type'],'random')

    async def test_cancelled_setup_disconnects(self):
        class SlowClient(Client):
            async def connect(self): await asyncio.Event().wait()
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',SlowClient):
            with self.assertRaises(TimeoutError):
                await asyncio.wait_for(bridge.connect_board(),0.03)
        self.assertTrue(Client.last.disconnected)

if __name__=='__main__': unittest.main()

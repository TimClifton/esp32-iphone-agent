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
        with patch.object(bridge,'CONNECTED_CLIENT',client),patch.object(bridge,'REJECTED_CLIENT',None), \
             patch.object(bridge,'REJECTION_DISCONNECT',None),patch.object(bridge.sys,'stdout',type('Output',(),{'buffer':output})()):
            bridge.receive(None,b'\x82'+b'0'*16+b'wrong!'+b'0'*16)
            bridge.receive(None,b'should not be forwarded')
            await asyncio.sleep(0)
        client.disconnect.assert_awaited_once()
        self.assertEqual(output.getvalue()[2],0xE0)
        self.assertNotIn(b'should not be forwarded',output.getvalue())

    async def test_identity_refusal_is_terminal_even_if_notify_times_out(self):
        clients=[]
        class WrongClient(Client):
            def __init__(self,*args,**kwargs):
                super().__init__(*args,**kwargs)
                clients.append(self)
                self.disconnect_count=0
            async def start_notify(self,_,callback):
                callback(None,b'\x82'+b'0'*16+b'wrong!'+b'0'*16)
                callback(None,b'should not be forwarded')
                raise TimeoutError()
            async def disconnect(self):
                self.disconnect_count+=1
        output=io.BytesIO()
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',WrongClient), \
             patch.object(bridge,'REJECTED_CLIENT',None),patch.object(bridge,'REJECTION_DISCONNECT',None), \
             patch.object(bridge.sys,'stdout',type('Output',(),{'buffer':output})()):
            with self.assertRaisesRegex(RuntimeError,'Unexpected board identity'):
                await bridge.connect_board()
        self.assertEqual(len(clients),1)
        self.assertEqual(clients[0].disconnect_count,1)
        self.assertNotIn(b'should not be forwarded',output.getvalue())
    async def test_eof_disconnects_and_uses_random_cli_service(self):
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',Client),patch.object(bridge,'read_frame',lambda:None):
            await bridge.main()
        self.assertTrue(Client.last.disconnected)
        self.assertEqual(Client.last.options['services'],[bridge.BASE+'0010'])
        self.assertEqual(Client.last.options['winrt']['address_type'],'random')

    async def test_cancelled_setup_disconnects(self):
        attempts=[]
        class SlowClient(Client):
            async def connect(self):
                attempts.append(self)
                await asyncio.Event().wait()
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',SlowClient):
            with self.assertRaises(TimeoutError):
                await asyncio.wait_for(bridge.connect_board(),0.03)
        self.assertTrue(Client.last.disconnected)
        self.assertEqual(attempts, [Client.last])

    async def test_timeout_awaits_cleanup_then_uses_one_fresh_client(self):
        attempts=[]
        events=[]
        class RetryClient(Client):
            def __init__(self,device,**kwargs):
                super().__init__(device,**kwargs)
                self.number=len(attempts)
                attempts.append(self)
            async def connect(self):
                events.append(('connect', self.number))
            async def start_notify(self,uuid,callback):
                self.callback=callback
                if self.number == 0:
                    raise TimeoutError()
                events.append(('subscribe', self.number))
            async def disconnect(self):
                events.append(('disconnect', self.number))
                await super().disconnect()
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',RetryClient):
            client=await bridge.connect_board()
        self.assertIs(client,attempts[1])
        self.assertIsNot(attempts[0],attempts[1])
        self.assertEqual(events,[('connect',0),('disconnect',0),('connect',1),('subscribe',1)])
        with patch.object(bridge,'receive') as receive:
            attempts[0].callback(None,b'old')
            client.callback(None,b'current')
        receive.assert_called_once_with(None,b'current')

    async def test_cleanup_failure_prevents_a_second_client(self):
        attempts=[]
        class BrokenClient(Client):
            async def connect(self):
                attempts.append(self)
                raise TimeoutError()
            async def disconnect(self):
                raise OSError('cleanup failed')
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',BrokenClient):
            with self.assertRaises(TimeoutError):
                await bridge.connect_board()
        self.assertEqual(attempts,[Client.last])
        self.assertIsNone(bridge.CONNECTED_CLIENT)

    async def test_attempt_and_cleanup_share_the_global_deadline(self):
        budgets=[]
        disconnected=[]
        class TimedClient(Client):
            async def connect(self):
                budgets.append(self.options['timeout'])
                await asyncio.Event().wait()
            async def disconnect(self):
                disconnected.append(self)
        loop=asyncio.get_running_loop()
        start=loop.time()
        with patch.object(bridge,'BleakScanner',Scanner),patch.object(bridge,'BleakClient',TimedClient), \
             patch.object(bridge,'STARTUP_TIMEOUT',0.15),patch.object(bridge,'GATT_ATTEMPT_TIMEOUTS',(0.025,12)), \
             patch.object(bridge,'DISCONNECT_TIMEOUT',0.035):
            with self.assertRaises(TimeoutError):
                await bridge.connect_board()
        elapsed=loop.time()-start
        self.assertEqual(len(budgets),2)
        self.assertEqual(len(disconnected),2)
        self.assertLess(budgets[1],0.15-0.035-0.025)
        self.assertLess(elapsed,0.4)

if __name__=='__main__': unittest.main()

(() => {
  const NativeWebSocket = window.WebSocket;

  function DemoWebSocket(address, protocols) {
    const target = new URL(address, window.location.href);
    if (target.origin === window.location.origin && target.pathname === "/" && target.searchParams.get("action") === "stream") {
      target.pathname = "/demo/device/stream";
    }
    return protocols === undefined
      ? new NativeWebSocket(target.toString())
      : new NativeWebSocket(target.toString(), protocols);
  }

  DemoWebSocket.prototype = NativeWebSocket.prototype;
  Object.setPrototypeOf(DemoWebSocket, NativeWebSocket);
  window.WebSocket = DemoWebSocket;
})();

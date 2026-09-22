(() => {
  const NativeWebSocket = window.WebSocket;
  const nativeFetch = window.fetch.bind(window);
  const readOnlyAPI = new Set([
    "/api/capabilities",
    "/api/settings/device",
    "/api/devices/screen-state",
  ]);

  window.fetch = (resource, options) => {
    const requestURL = typeof resource === "string" || resource instanceof URL
      ? new URL(resource, window.location.href)
      : new URL(resource.url, window.location.href);
    if (requestURL.origin === window.location.origin && readOnlyAPI.has(requestURL.pathname)) {
      requestURL.pathname = `/demo/device${requestURL.pathname}`;
      if (resource instanceof Request) {
        resource = new Request(requestURL, resource);
      } else {
        resource = requestURL;
      }
    }
    return nativeFetch(resource, options);
  };

  function DemoWebSocket(address, protocols) {
    const target = new URL(address, window.location.href);
    if (target.host === window.location.host && target.pathname === "/" && target.searchParams.get("action") === "stream") {
      target.protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
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

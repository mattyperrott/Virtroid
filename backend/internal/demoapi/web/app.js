(() => {
  const startButton = document.querySelector("#start-demo");
  const endButton = document.querySelector("#end-demo");
  const sessionCard = document.querySelector("#session-card");
  const sessionDetail = document.querySelector("#session-detail");
  const sessionClock = document.querySelector("#session-clock");
  const device = document.querySelector("#android-device");
  const placeholder = document.querySelector("#device-placeholder");
  const stream = document.querySelector("#android-stream");
  const nodes = [...document.querySelectorAll(".node")];
  const paths = [...document.querySelectorAll(".path")];
  let expiresAt = 0;
  let countdownTimer;
  let statusTimer;
  let streamTimer;

  function request(path, options = {}) {
    return fetch(path, {
      credentials: "same-origin",
      cache: "no-store",
      ...options,
      headers: { "X-Virtroid-Demo": "1", ...(options.headers || {}) },
    });
  }

  function setFlow(step) {
    nodes.forEach((node, index) => node.classList.toggle("is-live", index <= step));
    paths.forEach((path, index) => path.classList.toggle("is-live", index < step));
  }

  function formatRemaining(milliseconds) {
    const seconds = Math.max(0, Math.ceil(milliseconds / 1000));
    return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
  }

  function startCountdown() {
    clearInterval(countdownTimer);
    const tick = () => {
      const remaining = expiresAt - Date.now();
      sessionClock.textContent = formatRemaining(remaining);
      if (remaining <= 0) {
        clearInterval(countdownTimer);
        stopStream("Session complete");
        window.setTimeout(checkStatus, 800);
      }
    };
    tick();
    countdownTimer = window.setInterval(tick, 1000);
  }

  function showReady() {
    sessionCard.hidden = false;
    startButton.disabled = false;
    startButton.hidden = false;
    endButton.hidden = true;
    sessionDetail.textContent = "One visitor at a time · eight-minute sessions";
    sessionClock.textContent = "08:00";
    setFlow(0);
  }

  function showBusy(availableAt) {
    sessionCard.hidden = true;
    startButton.disabled = true;
    startButton.hidden = true;
    endButton.hidden = true;
    const ready = availableAt ? new Date(availableAt) : null;
    sessionDetail.textContent = ready && !Number.isNaN(ready.valueOf())
      ? `Expected back by ${ready.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
      : "Checking again automatically";
    setFlow(0);
  }

  function showOffline() {
    sessionCard.hidden = true;
    startButton.disabled = true;
    startButton.hidden = true;
    endButton.hidden = true;
    sessionDetail.textContent = "The preview will enable automatically when Android is ready";
    setFlow(0);
  }

  async function checkStatus() {
    try {
      const response = await request("api/status");
      if (!response.ok) throw new Error("status unavailable");
      const status = await response.json();
      if (!status.configured || !status.ready) {
        showOffline();
      } else if (status.owned) {
        await beginSession();
      } else if (status.available) {
        showReady();
      } else {
        showBusy(status.expires_at);
      }
    } catch {
      showOffline();
    }
  }

  async function beginSession() {
    sessionCard.hidden = false;
    startButton.disabled = true;
    sessionDetail.textContent = "Establishing a low-latency browser stream";
    setFlow(1);

    try {
      const response = await request("api/session", { method: "POST" });
      const payload = await response.json();
      if (response.status === 423) {
        showBusy(payload.available_at);
        return;
      }
      if (response.status === 503) {
        showOffline();
        return;
      }
      if (!response.ok || !payload.embed_url) throw new Error("stream unavailable");

      expiresAt = Date.parse(payload.expires_at);
      stream.src = payload.embed_url;
      stream.hidden = false;
      placeholder.hidden = true;
      device.classList.add("is-streaming");
      startButton.hidden = true;
      endButton.hidden = false;
      sessionDetail.textContent = "Opening the handset pixel stream";
      setFlow(2);
      startCountdown();
    } catch {
      showOffline();
    }
  }

  function stopStream() {
    clearInterval(streamTimer);
    stream.removeAttribute("src");
    stream.hidden = true;
    placeholder.hidden = false;
    device.classList.remove("is-streaming");
    clearInterval(countdownTimer);
    expiresAt = 0;
  }

  async function endSession() {
    endButton.disabled = true;
    stopStream();
    try {
      await request("api/session/end", { method: "POST", keepalive: true });
    } finally {
      endButton.disabled = false;
      await checkStatus();
    }
  }

  startButton.addEventListener("click", beginSession);
  endButton.addEventListener("click", endSession);

  stream.addEventListener("load", () => {
    if (stream.hidden || !stream.getAttribute("src")) return;
    clearInterval(streamTimer);
    const startedAt = Date.now();
    streamTimer = window.setInterval(() => {
      try {
        const status = stream.contentDocument?.querySelector("#status");
        const message = status?.textContent || "";
        if (message.startsWith("connected") || status?.classList.contains("hidden")) {
          clearInterval(streamTimer);
          sessionDetail.textContent = "Tap inside the handset · keyboard input supported";
          setFlow(3);
        } else if (message.startsWith("error") || message.startsWith("disconnected")) {
          clearInterval(streamTimer);
          sessionDetail.textContent = "End the session and try again";
          setFlow(1);
        } else if (Date.now() - startedAt > 20000) {
          clearInterval(streamTimer);
          sessionDetail.textContent = "End the session and try again";
          setFlow(1);
        }
      } catch {
        // The viewer is intentionally same-origin; keep waiting during load.
      }
    }, 250);
  });

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && !expiresAt) checkStatus();
  });

  statusTimer = window.setInterval(() => {
    if (!expiresAt) checkStatus();
  }, 15000);
  window.addEventListener("pagehide", () => clearInterval(statusTimer));
  checkStatus();
})();

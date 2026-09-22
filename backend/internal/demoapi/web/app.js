(() => {
  const startButton = document.querySelector("#start-demo");
  const endButton = document.querySelector("#end-demo");
  const sessionStatus = document.querySelector("#session-status");
  const sessionDetail = document.querySelector("#session-detail");
  const sessionClock = document.querySelector("#session-clock");
  const device = document.querySelector("#android-device");
  const placeholder = document.querySelector("#device-placeholder");
  const deviceMessage = document.querySelector("#device-message");
  const stream = document.querySelector("#android-stream");
  const interactionKicker = document.querySelector("#interaction-kicker");
  const interactionTitle = document.querySelector("#interaction-title");
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
    document.querySelector(".node--gateway b").textContent = step >= 1 ? "Encrypted stream" : "Waiting";
    document.querySelector(".node--handset b").textContent = step >= 2 ? "Virtroid running" : "Available";
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
    startButton.disabled = false;
    startButton.hidden = false;
    endButton.hidden = true;
    sessionStatus.textContent = "Demo handset available";
    sessionDetail.textContent = "One visitor at a time · eight-minute sessions";
    deviceMessage.textContent = "Ready when you are.";
    interactionKicker.textContent = "REMOTE DEVICE";
    interactionTitle.textContent = "Standing by";
    sessionClock.textContent = "08:00";
    setFlow(0);
  }

  function showBusy(availableAt) {
    startButton.disabled = true;
    startButton.hidden = false;
    endButton.hidden = true;
    sessionStatus.textContent = "Another visitor is exploring";
    const ready = availableAt ? new Date(availableAt) : null;
    sessionDetail.textContent = ready && !Number.isNaN(ready.valueOf())
      ? `Expected back by ${ready.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" })}`
      : "Checking again automatically";
    deviceMessage.textContent = "One live session at a time.";
    interactionTitle.textContent = "Session occupied";
    setFlow(1);
  }

  function showOffline() {
    startButton.disabled = true;
    startButton.hidden = false;
    endButton.hidden = true;
    sessionStatus.textContent = "Demo handset is starting";
    sessionDetail.textContent = "The preview will enable automatically when Android is ready";
    deviceMessage.textContent = "Waking remote Android…";
    interactionTitle.textContent = "Starting handset";
    setFlow(1);
  }

  async function checkStatus() {
    try {
      const response = await request("api/status");
      if (!response.ok) throw new Error("status unavailable");
      const status = await response.json();
      if (!status.configured) {
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
    startButton.disabled = true;
    sessionStatus.textContent = "Reserving private demo handset…";
    sessionDetail.textContent = "Establishing a low-latency browser stream";
    deviceMessage.textContent = "Opening encrypted pixel stream…";
    interactionKicker.textContent = "CONNECTING";
    interactionTitle.textContent = "Negotiating stream";
    setFlow(1);

    try {
      const response = await request("api/session", { method: "POST" });
      const payload = await response.json();
      if (response.status === 423) {
        showBusy(payload.available_at);
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
      sessionStatus.textContent = "Live Android session reserved";
      sessionDetail.textContent = "Opening the handset pixel stream";
      interactionKicker.textContent = "CONNECTING";
      interactionTitle.textContent = "Starting Virtroid APK";
      setFlow(2);
      startCountdown();
    } catch {
      showOffline();
    }
  }

  function stopStream(message) {
    clearInterval(streamTimer);
    stream.removeAttribute("src");
    stream.hidden = true;
    placeholder.hidden = false;
    device.classList.remove("is-streaming");
    deviceMessage.textContent = message;
    clearInterval(countdownTimer);
    expiresAt = 0;
  }

  async function endSession() {
    endButton.disabled = true;
    stopStream("Resetting for the next visitor…");
    try {
      await request("api/session/end", { method: "POST", keepalive: true });
    } finally {
      endButton.disabled = false;
      showReady();
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
          interactionKicker.textContent = "LIVE ANDROID";
          interactionTitle.textContent = "Virtroid APK connected";
          setFlow(3);
        } else if (message.startsWith("error") || message.startsWith("disconnected")) {
          clearInterval(streamTimer);
          sessionStatus.textContent = "Android stream needs attention";
          sessionDetail.textContent = "End the session and try again";
          interactionKicker.textContent = "STREAM ERROR";
          interactionTitle.textContent = message;
          setFlow(1);
        } else if (Date.now() - startedAt > 20000) {
          clearInterval(streamTimer);
          sessionStatus.textContent = "Android stream timed out";
          sessionDetail.textContent = "End the session and try again";
          interactionTitle.textContent = "Handset did not respond";
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

  const clock = document.querySelector("#demo-clock");
  function updateClock() {
    clock.textContent = `${new Date().toISOString().slice(11, 19)} UTC`;
  }
  updateClock();
  window.setInterval(updateClock, 1000);

  statusTimer = window.setInterval(() => {
    if (!expiresAt) checkStatus();
  }, 15000);
  window.addEventListener("pagehide", () => clearInterval(statusTimer));
  checkStatus();
})();

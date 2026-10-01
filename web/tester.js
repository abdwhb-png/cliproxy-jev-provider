"use strict";

const keyInput = document.querySelector("#api-key");
const loadModelsButton = document.querySelector("#load-models");
const modelList = document.querySelector("#models");
const editor = document.querySelector("#request");
const exampleKind = document.querySelector("#example-kind");
const loadExampleButton = document.querySelector("#load-example");
const runButton = document.querySelector("#run");
const cancelButton = document.querySelector("#cancel");
const status = document.querySelector("#status");
const responseBody = document.querySelector("#response");

let aliases = new Set();
let catalogRequest = null;
let decisionRequest = null;

function updateControls() {
  const busy = catalogRequest !== null || decisionRequest !== null;
  loadModelsButton.disabled = busy || keyInput.value.trim() === "";
  runButton.disabled = busy || aliases.size === 0;
  loadExampleButton.disabled = busy || aliases.size === 0;
  cancelButton.disabled = decisionRequest === null;
}

function clearConnection() {
  catalogRequest?.abort();
  decisionRequest?.abort();
  catalogRequest = null;
  decisionRequest = null;
  aliases.clear();
  modelList.replaceChildren();
  responseBody.textContent = "";
  status.textContent = "Enter a client key and load models.";
  updateControls();
}

keyInput.value = "";
keyInput.addEventListener("input", clearConnection);
window.addEventListener("pagehide", () => {
  keyInput.value = "";
  clearConnection();
});
updateControls();

loadModelsButton.addEventListener("click", async () => {
  if (catalogRequest || decisionRequest || !keyInput.value.trim()) return;
  const controller = new AbortController();
  catalogRequest = controller;
  aliases.clear();
  modelList.replaceChildren();
  responseBody.textContent = "";
  status.textContent = "Loading models…";
  updateControls();
  try {
    const response = await fetch("/v1/models", {
      headers: { Authorization: `Bearer ${keyInput.value.trim()}` },
      credentials: "omit",
      signal: controller.signal,
    });
    const text = await response.text();
    if (catalogRequest !== controller) return;
    if (!response.ok) {
      status.textContent = `Model discovery: HTTP ${response.status}`;
      responseBody.textContent = text;
      return;
    }
    const catalog = JSON.parse(text);
    if (!Array.isArray(catalog.data)) throw new Error("Invalid model catalog returned by server.");
    const ids = catalog.data
      .filter((model) => model.owned_by === "typesafe" && typeof model.id === "string")
      .map((model) => model.id)
      .sort();
    aliases = new Set(ids);
    for (const id of aliases) {
      const item = document.createElement("li");
      const name = document.createElement("code");
      name.textContent = id;
      const copy = document.createElement("button");
      copy.type = "button";
      copy.textContent = "Copy";
      copy.setAttribute("aria-label", `Copy ${id}`);
      copy.addEventListener("click", async () => {
        try {
          await navigator.clipboard.writeText(id);
          status.textContent = `Copied ${id}.`;
        } catch {
          status.textContent = "Clipboard unavailable. Select the alias text to copy it.";
        }
      });
      item.append(name, copy);
      modelList.append(item);
    }
    status.textContent = aliases.size ? `${aliases.size} JEV models available.` : "No JEV models available.";
  } catch (error) {
    if (catalogRequest === controller && !controller.signal.aborted) {
      status.textContent = `Model discovery failed: ${error.message}`;
    }
  } finally {
    if (catalogRequest === controller) {
      catalogRequest = null;
      updateControls();
    }
  }
});

loadExampleButton.addEventListener("click", () => {
  if (aliases.size === 0 || catalogRequest || decisionRequest) return;
  const questions = {
    urgent: { type: "noul", instructions: "Does this require urgent attention?" },
    team: {
      type: "choice",
      instructions: { question: "Which team should handle this?" },
      criteria: { billing: "Payment problems", support: "Other problems" },
    },
    severity: {
      type: "score",
      instructions: "How severe is this?",
      criteria: ["Minor inconvenience", "Work blocked", "Critical outage"],
    },
  };
  const selectedQuestions = exampleKind.value === "all"
    ? questions
    : Object.fromEntries(Object.entries(questions).filter(([, question]) => question.type === exampleKind.value));
  editor.value = JSON.stringify({
    model: aliases.has("jev-1.13-free") ? "jev-1.13-free" : aliases.values().next().value,
    state: { ticket: "Payments have failed for three days." },
    questions: selectedQuestions,
  }, null, 2);
  status.textContent = "Example loaded. Edit the JSON, then run the decision.";
});

runButton.addEventListener("click", async () => {
  if (catalogRequest || decisionRequest || aliases.size === 0) return;
  const raw = editor.value;
  let request;
  try {
    request = JSON.parse(raw);
  } catch {
    status.textContent = "Invalid JSON. Correct the request before running.";
    return;
  }
  if (!request || typeof request !== "object" || Array.isArray(request) || !aliases.has(request.model)) {
    status.textContent = "Choose an available JEV alias in the model field.";
    return;
  }
  const controller = new AbortController();
  decisionRequest = controller;
  const started = performance.now();
  responseBody.textContent = "";
  status.textContent = "Running…";
  updateControls();
  try {
    const response = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: { Authorization: `Bearer ${keyInput.value.trim()}`, "Content-Type": "application/json" },
      credentials: "omit",
      body: raw,
      signal: controller.signal,
    });
    const text = await response.text();
    if (decisionRequest !== controller) return;
    responseBody.textContent = text;
    status.textContent = `HTTP ${response.status} · ${Math.round(performance.now() - started)} ms`;
  } catch (error) {
    if (decisionRequest === controller) {
      status.textContent = controller.signal.aborted ? "Cancelled." : `Request failed: ${error.message}`;
    }
  } finally {
    if (decisionRequest === controller) {
      decisionRequest = null;
      updateControls();
    }
  }
});

cancelButton.addEventListener("click", () => decisionRequest?.abort());

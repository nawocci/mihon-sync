// A browser-less check for the dashboard's show/hide logic: it stubs just enough
// DOM to load dashboard.html's inline script and drive the register and logout
// flows the way a phone or desktop browser would.
//
// Run with: node internal/server/dashboard.test.js
"use strict";

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

const html = fs.readFileSync(path.join(__dirname, "dashboard.html"), "utf8");
const script = html.match(/<script>([\s\S]*)<\/script>/)[1];

const ids = [
  "subtitle",
  "setup",
  "bootstrap",
  "bootstrap-label",
  "bootstrap-button",
  "register",
  "invite-code",
  "register-label",
  "register-button",
  "login",
  "login-key",
  "login-button",
  "account",
  "account-label",
  "account-created",
  "account-records",
  "pairing-url",
  "pairing-key",
  "copy-button",
  "logout-button",
  "error",
];

function makeElement(id, hidden) {
  return {
    id,
    hidden,
    textContent: "",
    value: "",
    listeners: new Map(),
    // The dashboard relies on the hidden attribute, not the class, so mismatches
    // against the markup fail loudly instead of silently staying invisible.
    toggleAttribute(name, force) {
      if (name === "hidden") this.hidden = Boolean(force);
    },
    addEventListener(type, handler) {
      if (!this.listeners.has(type)) this.listeners.set(type, []);
      this.listeners.get(type).push(handler);
    },
    async click() {
      for (const handler of this.listeners.get("click") || []) await handler();
    },
  };
}

function scenario({ status, key, me }) {
  const elements = new Map();
  for (const id of ids) {
    const tag = html.match(new RegExp(`<[^>]*id="${id}"[^>]*>`));
    elements.set(id, makeElement(id, tag ? /\shidden[\s>]/.test(tag[0]) : false));
  }

  const storage = new Map();
  const routes = {
    "GET /bootstrap/status": () => [200, status],
    "POST /v1/register": () => [200, key],
    "POST /bootstrap/key": () => [200, key],
    "GET /v1/me": () => [200, me],
  };

  const context = {
    document: { getElementById: (id) => elements.get(id) },
    localStorage: {
      getItem: (storageKey) => (storage.has(storageKey) ? storage.get(storageKey) : null),
      setItem: (storageKey, value) => storage.set(storageKey, String(value)),
      removeItem: (storageKey) => storage.delete(storageKey),
    },
    navigator: {},
    location: { origin: "http://127.0.0.1:8080", pathname: "/" },
    fetch: async (url, options = {}) => {
      const route = routes[`${options.method || "GET"} ${url}`];
      if (!route) {
        return { ok: false, status: 404, json: async () => ({ error: "not found" }) };
      }
      const [code, data] = route();
      return { ok: code >= 200 && code < 300, status: code, json: async () => data };
    },
    setTimeout,
  };

  vm.createContext(context);
  vm.runInContext(script, context);

  return {
    elements,
    flush: async () => {
      for (let i = 0; i < 20; i++) await new Promise((resolve) => setImmediate(resolve));
    },
  };
}

async function main() {
  // A LAN user: bootstrap is not allowed, so the register card is the entry point.
  const lan = scenario({
    status: { available: false, libraries: 0, version: "test" },
    key: { apiKey: "mhk_test", label: "key-1", createdAt: 0 },
    me: { label: "key-1", createdAt: 0, recordCount: 3 },
  });
  await lan.flush();
  assert.equal(lan.elements.get("register").hidden, false, "register visible before signing in");
  assert.equal(lan.elements.get("account").hidden, true, "account starts hidden");

  lan.elements.get("invite-code").value = "inv_x";
  await lan.elements.get("register-button").click();
  await lan.flush();

  assert.equal(lan.elements.get("register").hidden, true, "register hides after redeeming");
  assert.equal(lan.elements.get("login").hidden, true, "login hides after redeeming");
  assert.equal(lan.elements.get("account").hidden, false, "account must appear after redeeming");
  assert.equal(lan.elements.get("account-label").textContent, "key-1");
  assert.equal(lan.elements.get("pairing-key").textContent, "mhk_test");

  await lan.elements.get("logout-button").click();
  await lan.flush();
  assert.equal(lan.elements.get("register").hidden, false, "register returns after logout");
  assert.equal(lan.elements.get("login").hidden, false, "login returns after logout");
  assert.equal(lan.elements.get("account").hidden, true, "account hides after logout");

  // A loopback user: the bootstrap card appears when the server allows it.
  const local = scenario({
    status: { available: true, libraries: 0, version: "test" },
    key: { apiKey: "mhk_local", label: "key-1", createdAt: 0 },
    me: { label: "key-1", createdAt: 0, recordCount: 0 },
  });
  await local.flush();
  assert.equal(local.elements.get("bootstrap").hidden, false, "bootstrap appears when available");
  assert.equal(local.elements.get("setup").hidden, false, "first-key hint appears on an empty server");

  console.log("dashboard checks passed");
}

process.on("unhandledRejection", (error) => {
  console.error("unhandled rejection:", error);
  process.exit(1);
});

main().catch((error) => {
  console.error(error);
  process.exit(1);
});

/* Shield Agent - Web UI */
(function () {
  "use strict";

  // ---------------------------------------------------------------------------
  // Helpers
  // ---------------------------------------------------------------------------

  function fetchAPI(path, opts) {
    opts = opts || {};
    opts.credentials = "same-origin";
    opts.headers = Object.assign({ "Content-Type": "application/json" }, opts.headers || {});
    return fetch("/api" + path, opts).then(function (res) {
      if (res.status === 401) {
        showLogin();
        return Promise.reject(new Error("Unauthorized"));
      }
      if (!res.ok) {
        return res.text().then(function (t) {
          return Promise.reject(new Error(t || res.statusText));
        });
      }
      var ct = res.headers.get("content-type") || "";
      if (ct.indexOf("application/json") !== -1) {
        return res.json();
      }
      return res.text();
    });
  }

  function el(tag, attrs, children) {
    var node = document.createElement(tag);
    if (attrs) {
      Object.keys(attrs).forEach(function (k) {
        if (k === "className") {
          node.className = attrs[k];
        } else if (k.indexOf("on") === 0) {
          node.addEventListener(k.slice(2).toLowerCase(), attrs[k]);
        } else {
          node.setAttribute(k, attrs[k]);
        }
      });
    }
    (children || []).forEach(function (c) {
      if (typeof c === "string") {
        node.appendChild(document.createTextNode(c));
      } else if (c) {
        node.appendChild(c);
      }
    });
    return node;
  }

  function html(parent, content) {
    parent.innerHTML = content;
  }

  // ---------------------------------------------------------------------------
  // DOM references
  // ---------------------------------------------------------------------------

  var loginOverlay = document.getElementById("login-overlay");
  var loginForm = document.getElementById("login-form");
  var loginPassword = document.getElementById("login-password");
  var loginError = document.getElementById("login-error");
  var appEl = document.getElementById("app");
  var contentEl = document.getElementById("content");
  var pageTitle = document.getElementById("page-title");
  var sidebar = document.getElementById("sidebar");
  var menuOpen = document.getElementById("menu-open");
  var menuClose = document.getElementById("menu-close");
  var logoutBtn = document.getElementById("logout-btn");
  var navLinks = document.querySelectorAll(".nav-link");

  // ---------------------------------------------------------------------------
  // Auth
  // ---------------------------------------------------------------------------

  function showLogin() {
    loginOverlay.hidden = false;
    appEl.hidden = true;
    loginPassword.value = "";
    loginError.hidden = true;
    stopAutoRefresh();
  }

  function showApp() {
    loginOverlay.hidden = true;
    appEl.hidden = false;
    route();
  }

  loginForm.addEventListener("submit", function (e) {
    e.preventDefault();
    var pw = loginPassword.value.trim();
    if (!pw) return;
    fetchAPI("/auth/login", {
      method: "POST",
      body: JSON.stringify({ password: pw }),
    })
      .then(function () {
        showApp();
      })
      .catch(function (err) {
        loginError.textContent = err.message || "Login failed";
        loginError.hidden = false;
      });
  });

  logoutBtn.addEventListener("click", function () {
    fetchAPI("/auth/logout", { method: "POST" }).catch(function () {});
    showLogin();
  });

  // ---------------------------------------------------------------------------
  // Sidebar mobile toggle
  // ---------------------------------------------------------------------------

  menuOpen.addEventListener("click", function () {
    sidebar.classList.add("open");
  });

  menuClose.addEventListener("click", function () {
    sidebar.classList.remove("open");
  });

  // Close sidebar on nav click (mobile)
  navLinks.forEach(function (link) {
    link.addEventListener("click", function () {
      sidebar.classList.remove("open");
    });
  });

  // ---------------------------------------------------------------------------
  // Router
  // ---------------------------------------------------------------------------

  var refreshTimer = null;

  function stopAutoRefresh() {
    if (refreshTimer) {
      clearInterval(refreshTimer);
      refreshTimer = null;
    }
  }

  function route() {
    stopAutoRefresh();
    var hash = (location.hash || "#dashboard").slice(1);
    var pages = { dashboard: pageDashboard, logs: pageLogs, tokens: pageTokens, settings: pageSettings };
    var fn = pages[hash] || pageDashboard;

    // Update active nav
    navLinks.forEach(function (link) {
      var page = link.getAttribute("data-page");
      if (page === hash) {
        link.classList.add("active");
      } else {
        link.classList.remove("active");
      }
    });

    var titles = { dashboard: "Dashboard", logs: "Logs", tokens: "Tokens", settings: "Settings" };
    pageTitle.textContent = titles[hash] || "Dashboard";

    contentEl.innerHTML = "";
    fn();
  }

  window.addEventListener("hashchange", route);

  // ---------------------------------------------------------------------------
  // Pages
  // ---------------------------------------------------------------------------

  // -- Dashboard --
  function pageDashboard() {
    html(contentEl, '<div class="loading">Loading dashboard...</div>');

    function loadDashboard() {
      fetchAPI("/dashboard")
        .then(function (data) {
          contentEl.innerHTML = "";
          var grid = el("div", { className: "stats-grid" });

          var cards = [
            { label: "Total Requests", value: data.total_requests != null ? data.total_requests : "--" },
            { label: "Error Rate", value: data.error_rate != null ? (data.error_rate * 100).toFixed(1) + "%" : "--" },
            { label: "Avg Latency", value: data.avg_latency_ms != null ? data.avg_latency_ms.toFixed(1) + " ms" : "--" },
            { label: "Active Tokens", value: data.active_tokens != null ? data.active_tokens : "--" },
          ];

          cards.forEach(function (c) {
            grid.appendChild(
              el("div", { className: "stat-card" }, [
                el("div", { className: "label" }, [c.label]),
                el("div", { className: "value" }, [String(c.value)]),
              ])
            );
          });

          contentEl.appendChild(grid);
        })
        .catch(function () {
          html(contentEl, '<div class="empty">Failed to load dashboard data.</div>');
        });
    }

    loadDashboard();
    refreshTimer = setInterval(loadDashboard, 10000);
  }

  // -- Logs --
  function pageLogs() {
    html(contentEl, '<div class="loading">Loading logs...</div>');

    fetchAPI("/logs?last=50")
      .then(function (data) {
        var logs = Array.isArray(data) ? data : data.logs || [];
        if (logs.length === 0) {
          html(contentEl, '<div class="empty">No log entries yet.</div>');
          return;
        }

        var wrap = el("div", { className: "table-wrap" });
        var table = el("table");
        table.appendChild(
          el("thead", null, [
            el("tr", null, [
              el("th", null, ["Timestamp"]),
              el("th", null, ["Direction"]),
              el("th", null, ["Method"]),
              el("th", null, ["Success"]),
              el("th", null, ["Latency"]),
              el("th", null, ["IP"]),
              el("th", null, ["Auth"]),
            ]),
          ])
        );

        var tbody = el("tbody");
        logs.forEach(function (log) {
          var success = log.success || log.status_ok;
          var latency = log.latency_ms != null ? log.latency_ms.toFixed(1) + " ms" : log.latency || "--";
          var ts = log.timestamp || log.time || "--";
          if (ts !== "--" && ts.length > 19) {
            ts = ts.replace("T", " ").slice(0, 19);
          }

          tbody.appendChild(
            el("tr", null, [
              el("td", null, [ts]),
              el("td", null, [log.direction || "--"]),
              el("td", null, [log.method || "--"]),
              el("td", null, [
                el("span", { className: "badge " + (success ? "badge-ok" : "badge-fail") }, [success ? "OK" : "FAIL"]),
              ]),
              el("td", null, [String(latency)]),
              el("td", null, [log.ip || log.remote_addr || "--"]),
              el("td", null, [log.auth || log.auth_type || "--"]),
            ])
          );
        });

        table.appendChild(tbody);
        wrap.appendChild(table);
        contentEl.innerHTML = "";
        contentEl.appendChild(wrap);
      })
      .catch(function () {
        html(contentEl, '<div class="empty">Failed to load logs.</div>');
      });
  }

  // -- Tokens --
  function pageTokens() {
    html(contentEl, '<div class="loading">Loading tokens...</div>');
    loadTokens();
  }

  function loadTokens() {
    fetchAPI("/tokens")
      .then(function (data) {
        var tokens = Array.isArray(data) ? data : data.tokens || [];
        contentEl.innerHTML = "";

        // Create form
        var form = el("div", { className: "form-section" }, [
          el("h4", null, ["Create Token"]),
        ]);
        var row = el("div", { className: "form-row" });

        var nameInput = el("input", { type: "text", placeholder: "Token name", id: "token-name" });
        row.appendChild(el("div", { className: "form-group" }, [el("label", { for: "token-name" }, ["Name"]), nameInput]));

        var expiryInput = el("input", { type: "text", placeholder: "e.g. 24h, 7d", id: "token-expiry" });
        row.appendChild(el("div", { className: "form-group" }, [el("label", { for: "token-expiry" }, ["Expiry"]), expiryInput]));

        var createBtn = el("button", { className: "btn btn-primary", onClick: handleCreateToken }, ["Create"]);
        row.appendChild(el("div", { className: "form-group" }, [el("label", null, ["\u00A0"]), createBtn]));

        form.appendChild(row);

        var createMsg = el("p", { id: "create-msg", style: "margin-top:0.75rem;font-size:0.85rem;word-break:break-all" });
        form.appendChild(createMsg);

        contentEl.appendChild(form);

        function handleCreateToken() {
          var name = nameInput.value.trim();
          if (!name) return;
          var body = { name: name };
          var exp = expiryInput.value.trim();
          if (exp) body.expiry = exp;

          fetchAPI("/tokens", { method: "POST", body: JSON.stringify(body) })
            .then(function (res) {
              createMsg.style.color = "#2ecc71";
              createMsg.textContent = "Token created" + (res.token ? ": " + res.token : "") + (res.key ? ": " + res.key : "");
              nameInput.value = "";
              expiryInput.value = "";
              loadTokens();
            })
            .catch(function (err) {
              createMsg.style.color = "#e94560";
              createMsg.textContent = "Error: " + err.message;
            });
        }

        // Tokens table
        if (tokens.length === 0) {
          contentEl.appendChild(el("div", { className: "empty" }, ["No tokens found."]));
          return;
        }

        var wrap = el("div", { className: "table-wrap" });
        var table = el("table");
        table.appendChild(
          el("thead", null, [
            el("tr", null, [
              el("th", null, ["ID"]),
              el("th", null, ["Name"]),
              el("th", null, ["Created"]),
              el("th", null, ["Expires"]),
              el("th", null, ["Status"]),
              el("th", null, ["Actions"]),
            ]),
          ])
        );

        var tbody = el("tbody");
        tokens.forEach(function (tok) {
          var id = tok.id || tok.ID || "--";
          var revoked = tok.revoked || tok.disabled;

          var revokeBtn = el(
            "button",
            {
              className: "btn btn-danger",
              onClick: function () {
                if (!confirm("Revoke token " + id + "?")) return;
                fetchAPI("/tokens/" + id, { method: "DELETE" })
                  .then(function () {
                    loadTokens();
                  })
                  .catch(function (err) {
                    alert("Failed: " + err.message);
                  });
              },
            },
            ["Revoke"]
          );

          var created = tok.created_at || tok.created || "--";
          if (created !== "--" && created.length > 10) created = created.slice(0, 10);

          var expires = tok.expires_at || tok.expires || "Never";
          if (expires !== "Never" && expires.length > 10) expires = expires.slice(0, 10);

          tbody.appendChild(
            el("tr", null, [
              el("td", null, [String(id)]),
              el("td", null, [tok.name || "--"]),
              el("td", null, [created]),
              el("td", null, [expires]),
              el("td", null, [
                el("span", { className: "badge " + (revoked ? "badge-fail" : "badge-ok") }, [revoked ? "Revoked" : "Active"]),
              ]),
              el("td", null, [revoked ? el("span", null, ["--"]) : revokeBtn]),
            ])
          );
        });

        table.appendChild(tbody);
        wrap.appendChild(table);
        contentEl.appendChild(wrap);
      })
      .catch(function () {
        html(contentEl, '<div class="empty">Failed to load tokens.</div>');
      });
  }

  // -- Settings (Middlewares) --
  function pageSettings() {
    html(contentEl, '<div class="loading">Loading settings...</div>');

    fetchAPI("/middlewares")
      .then(function (data) {
        var middlewares = Array.isArray(data) ? data : data.middlewares || [];
        contentEl.innerHTML = "";

        if (middlewares.length === 0) {
          html(contentEl, '<div class="empty">No middleware configuration found.</div>');
          return;
        }

        var wrap = el("div", { className: "table-wrap" });
        var table = el("table");
        table.appendChild(
          el("thead", null, [
            el("tr", null, [
              el("th", null, ["Middleware"]),
              el("th", null, ["Status"]),
              el("th", null, ["Actions"]),
            ]),
          ])
        );

        var tbody = el("tbody");
        middlewares.forEach(function (mw) {
          var name = mw.name || mw.Name || "--";
          var enabled = mw.enabled != null ? mw.enabled : mw.Enabled;

          var toggleBtn = el(
            "button",
            {
              className: "btn-toggle " + (enabled ? "on" : "off"),
              onClick: function () {
                fetchAPI("/middlewares/" + encodeURIComponent(name) + "/toggle", { method: "POST" })
                  .then(function () {
                    pageSettings();
                  })
                  .catch(function (err) {
                    alert("Toggle failed: " + err.message);
                  });
              },
            },
            [enabled ? "Enabled" : "Disabled"]
          );

          tbody.appendChild(
            el("tr", null, [
              el("td", null, [name]),
              el("td", null, [
                el("span", { className: "badge " + (enabled ? "badge-ok" : "badge-fail") }, [enabled ? "ON" : "OFF"]),
              ]),
              el("td", null, [toggleBtn]),
            ])
          );
        });

        table.appendChild(tbody);
        wrap.appendChild(table);
        contentEl.appendChild(wrap);
      })
      .catch(function () {
        html(contentEl, '<div class="empty">Failed to load middleware settings.</div>');
      });
  }

  // ---------------------------------------------------------------------------
  // Init - try to access dashboard; if 401 show login, else show app
  // ---------------------------------------------------------------------------

  fetchAPI("/dashboard")
    .then(function () {
      showApp();
    })
    .catch(function () {
      showLogin();
    });
})();

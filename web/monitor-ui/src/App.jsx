import React, { useEffect, useState } from "react";
import { Card } from "./components/ui/card";
import { Input } from "./components/ui/input";
import { Button } from "./components/ui/button";
import { Navigate, Route, Routes, useLocation, useParams } from "react-router-dom";
import { AppShell } from "./components/AppShell";
import { RealtimeProvider } from "./components/RealtimeProvider";
import { apiPaths, MONITOR_TOKEN_KEY, postJSON, requestJSON } from "./lib/api";
import { useI18n } from "./lib/i18n";
import { AccessPage } from "./routes/AccessPage";
import { ProviderDetailPage } from "./routes/ChannelDetailPage";
import { ProvidersPage } from "./routes/ChannelsPage";
import { EventsPage } from "./routes/EventsPage";
import { ModelDetailPage } from "./routes/ModelDetailPage";
import { ModelsPage } from "./routes/ModelsPage";
import { OverviewPage } from "./routes/OverviewPage";
import { QualityPage } from "./routes/QualityPage";
import { RoutingPage } from "./routes/RoutingPage";
import { SessionDetailPage } from "./routes/SessionDetailPage";
import { SystemPage } from "./routes/SystemPage";
import { TraceDetailPage } from "./routes/TraceDetailPage";
import { TrafficPage } from "./routes/TrafficPage";
import { UpstreamDetailPage } from "./routes/UpstreamDetailPage";

function App() {
  const { t } = useI18n();
  const location = useLocation();
  const [auth, setAuth] = useState({ loading: true, required: false, authorized: false, error: "", user: null });
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");

  useEffect(() => {
    let cancelled = false;

    async function resolveAuth() {
      try {
        const status = await requestJSON(apiPaths.authStatus);
        if (!status.auth_required) {
          if (!cancelled) {
            setAuth({ loading: false, required: false, authorized: true, error: "", user: { username: "local", role: "local", scope: "all" } });
          }
          return;
        }
        try {
          await requestJSON(apiPaths.authCheck);
          const user = await requestJSON(apiPaths.authMe).catch(() => null);
          if (!cancelled) {
            setAuth({ loading: false, required: true, authorized: true, error: "", user });
          }
        } catch {
          if (!cancelled) {
            setAuth({ loading: false, required: true, authorized: false, error: t("auth.invalidToken"), user: null });
          }
        }
      } catch (error) {
        if (!cancelled) {
          setAuth({ loading: false, required: true, authorized: false, error: error.message || t("auth.verifyFailed"), user: null });
        }
      }
    }

    resolveAuth();
    return () => {
      cancelled = true;
    };
  }, [t]);

  const submitToken = async (event) => {
    event.preventDefault();
    if (username.trim() && password) {
      try {
        const payload = await postJSON(apiPaths.authLogin, { username, password });
        window.localStorage.setItem(MONITOR_TOKEN_KEY, payload.token);
        const user = await requestJSON(apiPaths.authMe).catch(() => ({ username: username.trim(), role: "admin", scope: "all" }));
        setAuth({ loading: false, required: true, authorized: true, error: "", user });
        setPassword("");
      } catch {
        setAuth({ loading: false, required: true, authorized: false, error: t("auth.invalidCredentials"), user: null });
      }
      return;
    }
    setAuth({ loading: false, required: true, authorized: false, error: t("auth.enterCredentials"), user: null });
  };

  const logout = () => {
    window.localStorage.removeItem(MONITOR_TOKEN_KEY);
    setAuth({ loading: false, required: true, authorized: false, error: t("auth.signedOut"), user: null });
  };

  if (auth.loading) {
    return <div className="auth-screen"><div className="auth-panel"><p className="eyebrow">{t("auth.eyebrow")}</p><h1>{t("auth.checking")}</h1></div></div>;
  }

  if (auth.required && !auth.authorized) {
    return (
      <div className="auth-screen">
        <form className="auth-panel" onSubmit={submitToken}>
          <p className="eyebrow">{t("auth.eyebrow")}</p>
          <h1>{t("auth.signInTitle")}</h1>
          <label htmlFor="monitor-username">{t("auth.username")}</label>
          <Input id="monitor-username" type="text" autoComplete="username" value={username} onChange={(event) => setUsername(event.target.value)} autoFocus />
          <label htmlFor="monitor-password">{t("auth.password")}</label>
          <Input id="monitor-password" type="password" autoComplete="current-password" value={password} onChange={(event) => setPassword(event.target.value)} />
          {auth.error ? <p className="auth-error">{auth.error}</p> : null}
          <Button variant="primary" type="submit">{t("auth.signIn")}</Button>
        </form>
      </div>
    );
  }

  return (
    // Inside the authenticated branch on purpose: an unauthenticated console has
    // nothing to subscribe to, and the socket's token comes from the login.
    <RealtimeProvider>
      <AppShell user={auth.user} onLogout={logout}>
        <MonitorErrorBoundary key={location.pathname}>
        <Routes>
          <Route path="/" element={<Navigate to="/overview" replace />} />
          <Route path="/overview" element={<OverviewPage />} />

          <Route path="/traces" element={<TrafficPage />} />
          <Route path="/traces/:traceID" element={<TraceDetailPage />} />
          <Route path="/sessions/:sessionID" element={<SessionDetailPage />} />
          <Route path="/events" element={<EventsPage />} />
          <Route path="/audit" element={<QualityPage />} />

          <Route path="/providers" element={<ProvidersPage />} />
          <Route path="/providers/:providerID" element={<ProviderDetailPage />} />
          <Route path="/models" element={<ModelsPage />} />
          <Route path="/models/:model" element={<ModelDetailPage />} />
          <Route path="/routing" element={<RoutingPage />} />
          <Route path="/connect" element={<AccessPage />} />

          <Route path="/system" element={<SystemPage />} />
          <Route path="/upstreams/:upstreamID" element={<UpstreamDetailPage />} />

          {/* Pre-redesign addresses. Everything below redirects, preserving the
              query string, so bookmarks, external links and the trace detail
              "back" links keep working. */}
          <Route path="/requests" element={<LegacyRedirect to="/traces" />} />
          <Route path="/sessions" element={<LegacyRedirect to="/traces" tab="sessions" />} />
          <Route path="/analysis" element={<LegacyRedirect to="/audit" tab="analysis" />} />
          <Route path="/tokens" element={<LegacyRedirect to="/connect" tab="tokens" />} />
          <Route path="/channels" element={<LegacyRedirect to="/providers" />} />
          <Route path="/channels/:channelID" element={<LegacyRedirect to="/providers/:channelID" />} />
          <Route path="*" element={<Navigate to="/overview" replace />} />
        </Routes>
        </MonitorErrorBoundary>
      </AppShell>
    </RealtimeProvider>
  );
}

// Tab state lives in the query string, so a redirect to a merged page has to
// carry both the original parameters and the tab that replaces the old page.
function LegacyRedirect({ to, tab }) {
  const location = useLocation();
  const params = useParams();
  const path = Object.entries(params).reduce(
    (resolved, [key, value]) => resolved.replace(`:${key}`, encodeURIComponent(value ?? "")),
    to,
  );
  const search = new URLSearchParams(location.search);
  if (tab) {
    search.set("tab", tab);
  }
  const query = search.toString();
  return <Navigate to={query ? `${path}?${query}` : path} replace />;
}

class MonitorErrorBoundary extends React.Component {
  constructor(props) {
    super(props);
    this.state = { error: null };
  }

  static getDerivedStateFromError(error) {
    return { error };
  }

  componentDidCatch(error) {
    console.error("Monitor page render failed", error);
  }

  render() {
    if (this.state.error) {
      return (
        <div className="shell shell-list">
          <Card as="section">
            <div className="panel-head">
              <div>
                <h2>Unable to render this page</h2>
              </div>
            </div>
            <p className="event-message">{this.state.error.message || "The monitor UI hit a rendering error."}</p>
          </Card>
        </div>
      );
    }
    return this.props.children;
  }
}

export default App;

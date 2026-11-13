import React, { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronRight, Lock, X } from "lucide-react";
import { Card } from "../../components/ui/card";
import { Button } from "../../components/ui/button";
import { InlineTag } from "../../components/common/Badges";
import { EmptyState } from "../../components/common/EmptyState";
import { StatCard } from "../../components/common/Display";
import { XirStructureMap } from "../../components/monitor/XirStructureMap";
import { DetectorPluginSwitch } from "../../components/monitor/DetectorPluginSwitch";
import { DetectorPluginConflictDialog } from "../../components/monitor/DetectorPluginConflictDialog";
import { useJSON } from "../../hooks/useJSON";
import { apiPaths } from "../../lib/api";
import { useI18n } from "../../lib/i18n";
import { useDetectorPluginToggle } from "../../hooks/useDetectorPluginToggle";
import { localizedName as pluginName, orderedDetectorLanes } from "../../lib/plugins";
import {
  buildPointPluginIndex, buildStructureIndex, hookedNodeIds,
  localizedName, localizedDescription, pluginColor, pluginsForNode, structureStats,
} from "../../lib/xirStructure";

/*
 * XirStructurePanel is 质量 → XIR 结构: a static, protocol-neutral picture of
 * the Exchange IR plus the detection-plugin hook overlay. It deliberately reads
 * no traffic: the structure comes from GET /api/xir/structure (metadata only)
 * and the plugin state from the existing /api/detector-plugins catalog, so the
 * switches here are the same writes the plugins page makes — enabling a plugin
 * lights up its hook points on the map immediately.
 *
 * A non-admin reader still gets the structure and the point vocabulary; the
 * plugin catalog is admin-only, and its absence degrades to a note rather than
 * an error page.
 */
export function XirStructurePanel() {
  const { t, language } = useI18n();
  const structure = useJSON(apiPaths.xirStructure);
  const pluginsQuery = useJSON(apiPaths.detectorPlugins);
  const { busyID, conflict, setConflict, enable, requestDisable, onToggle } = useDetectorPluginToggle();

  const index = useMemo(() => buildStructureIndex(structure.data), [structure.data]);
  const items = pluginsQuery.data?.items || [];
  const pluginIndex = useMemo(() => buildPointPluginIndex(items), [items]);
  const stats = useMemo(() => structureStats(index, pluginIndex), [index, pluginIndex]);
  const enabledCount = items.filter((item) => item.enabled).length;

  const [focusId, setFocusId] = useState("");
  const [selectedId, setSelectedId] = useState("");

  const focusPlugin = items.find((item) => item.id === focusId) || null;
  const selectedNode = selectedId ? index.byId.get(selectedId) || null : null;
  const pointInfo = selectedNode?.point
    ? index.points.find((info) => info.point === selectedNode.point)
    : null;
  const selectedActing = selectedNode ? pluginsForNode(selectedNode, pluginIndex) : [];

  if (structure.error) {
    return <EmptyState title={t("xir.loadError")} detail={structure.error} tone="danger" />;
  }
  if (structure.loading && !structure.data) {
    return <EmptyState title={t("xir.loading")} detail={t("xir.loadingDetail")} />;
  }

  const pluginsUnavailable = Boolean(pluginsQuery.error) && !pluginsQuery.data;

  return (
    <>
      <div className="hero-grid hero-grid-compact xir-stats">
        <StatCard label={t("xir.statNodes")} value={stats.nodeCount} detail={t("xir.statNodesDetail")} />
        <StatCard label={t("xir.statPoints")} value={stats.pointCount} detail={t("xir.statPointsDetail")} />
        <StatCard
          label={t("xir.statPlugins")}
          value={pluginsUnavailable ? "—" : `${enabledCount}/${items.length}`}
          detail={t("xir.statPluginsDetail")}
          accent={enabledCount ? "accent-green" : ""}
        />
        <StatCard
          label={t("xir.statCoverage")}
          value={`${stats.hookedNodeCount}/${stats.pointNodeCount}`}
          detail={t("xir.statCoverageDetail")}
          accent="accent-gold"
        />
      </div>

      <div className="xir-layout">
        <Card as="section" className="xir-map-card">
          <XirStructureMap
            index={index}
            pluginIndex={pluginIndex}
            focusPlugin={focusPlugin}
            selectedId={selectedId}
            onSelect={(id) => setSelectedId((prev) => (prev === id ? "" : id))}
          />
        </Card>

        <aside className="xir-rail" aria-label={t("xir.railAria")}>
          {selectedNode ? (
            <Card className="xir-detail">
              <div className="xir-detail-head">
                <p className="eyebrow">{t("xir.nodeDetail")}</p>
                <Button variant="subtle" size="icon-sm" onClick={() => setSelectedId("")} aria-label={t("common.close")} title={t("common.close")}>
                  <X size={14} aria-hidden="true" />
                </Button>
              </div>
              <h3 className="xir-detail-name">{localizedName(selectedNode, language)}</h3>
              <code className="xir-detail-type">{selectedNode.type}</code>
              <p className="xir-detail-desc">{localizedDescription(selectedNode, language)}</p>
              {selectedNode.point ? (
                <div className="xir-detail-point">
                  <code className="xir-point">{selectedNode.point}</code>
                  {pointInfo?.reserved ? <InlineTag tone="gold">{t("xir.pointReserved")}</InlineTag> : null}
                  {pointInfo ? <p>{language === "en" ? pointInfo.description : pointInfo.description_zh || pointInfo.description}</p> : null}
                  {pointInfo?.reserved ? <p className="xir-detail-nopoint">{t("xir.pointReservedNote")}</p> : null}
                </div>
              ) : (
                <p className="xir-detail-nopoint">
                  {selectedNode.flags?.includes("sealed") ? <Lock size={12} aria-hidden="true" /> : null}
                  {selectedNode.flags?.includes("sealed") ? t("xir.sealedNote") : t("xir.noPointNote")}
                </p>
              )}
              {selectedNode.flags?.includes("media") ? <p className="xir-detail-nopoint">{t("xir.mediaNote")}</p> : null}
              {selectedNode.flags?.includes("recursive") ? <p className="xir-detail-nopoint">{t("xir.recursiveNote")}</p> : null}
              <div className="xir-detail-plugins">
                <h4>{t("xir.pluginsOnNode")}</h4>
                {selectedActing.length ? (
                  <ul>
                    {selectedActing.map((plugin) => (
                      <li key={plugin.id}>
                        <i style={{ background: pluginColor(plugin.id) }} aria-hidden="true" />
                        <span>{pluginName(plugin, language)}</span>
                        <InlineTag tone={plugin.enabled ? "green" : "default"}>
                          {plugin.enabled ? t("xir.pluginOn") : t("xir.pluginOff")}
                        </InlineTag>
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="xir-detail-nopoint">{t("xir.noPluginOnNode")}</p>
                )}
              </div>
            </Card>
          ) : null}

          <Card className="xir-plugin-rail">
            <div className="xir-rail-head">
              <h2>{t("xir.pluginRail")}</h2>
              {!pluginsUnavailable ? <InlineTag tone="accent">{t("xir.enabledCount", { enabled: enabledCount, total: items.length })}</InlineTag> : null}
            </div>
            <p className="xir-rail-hint">{t("xir.pluginRailHint")}</p>
            {pluginsUnavailable ? (
              <EmptyState compact title={t("xir.pluginsAdminOnly")} detail={pluginsQuery.error} />
            ) : (
              <div className="xir-plugin-list">
                {items.map((item, ordinal) => {
                  const focused = focusId === item.id;
                  const color = pluginColor(item.id, ordinal);
                  return (
                    <div
                      key={item.id}
                      className={`xir-plugin ${focused ? "is-focused" : ""} ${item.enabled ? "" : "is-off"}`}
                      style={{ "--plugin-color": color }}
                      role="button"
                      tabIndex={0}
                      aria-pressed={focused}
                      aria-label={t("xir.focusPluginAria", { name: pluginName(item, language) })}
                      onClick={() => setFocusId(focused ? "" : item.id)}
                      onKeyDown={(event) => {
                        if (event.target !== event.currentTarget) return;
                        if (event.key === "Enter" || event.key === " ") {
                          event.preventDefault();
                          setFocusId(focused ? "" : item.id);
                        }
                      }}
                    >
                      <span className="xir-plugin-dot" aria-hidden="true" />
                      <span className="xir-plugin-text">
                        <span className="xir-plugin-name">{pluginName(item, language)}</span>
                        <span className="xir-plugin-meta">
                          {item.contract ? <InlineTag tone="default">{t(`plugins.contract.${item.contract}`)}</InlineTag> : null}
                          {orderedDetectorLanes(item.lanes).map((lane) => (
                            <InlineTag key={lane} tone={lane === "hot" ? "gold" : "default"}>{t(`plugins.lane.${lane}`)}</InlineTag>
                          ))}
                          <em>{t("xir.pointCount", { count: (item.points || []).length })}</em>
                        </span>
                      </span>
                      <DetectorPluginSwitch
                        item={item}
                        disabled={busyID === item.id || enable.isPending}
                        onChange={(next) => onToggle(item, next)}
                      />
                    </div>
                  );
                })}
              </div>
            )}
            {focusPlugin ? (
              <Button variant="ghost" size="sm" className="xir-focus-clear" onClick={() => setFocusId("")}>
                {t("xir.clearFocus")}
              </Button>
            ) : null}
            <p className="xir-scope-note">{t("xir.scopeNote")}</p>
            <div className="xir-rail-foot">
              <Button asChild variant="link" size="sm">
                <Link to="/plugins?tab=detection">
                  {t("xir.managePlugins")}
                  <ChevronRight size={13} aria-hidden="true" />
                </Link>
              </Button>
            </div>
          </Card>
        </aside>
      </div>

      <DetectorPluginConflictDialog
        conflict={conflict}
        busy={conflict ? busyID === conflict.plugin.id : false}
        onCancel={() => setConflict(null)}
        onConfirm={() => requestDisable(conflict.plugin, true)}
      />
    </>
  );
}

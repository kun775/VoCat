import { useEffect, useState } from "react";
import { FieldRow } from "./FieldRow";
import type { DeviceDetail } from "./types";
import type { PublicIPInfo } from "../../types";
import { useI18n } from "../../lib/i18n";
import { api, apiMessage } from "../../api";
import { Button, Input, message } from "../ui";
import { CountryFlag } from "../CountryFlag";
import { formatBytes } from "../../lib/utils";

const MIN_CONSUME_KB = 1;
const MAX_CONSUME_KB = 10240;

interface DataConsumeResult {
  requestedBytes: number;
  payloadBytes: number;
  interfaceMeasured: boolean;
  interfaceRxBytes: number;
  interfaceTxBytes: number;
  complete: boolean;
  durationMs: number;
}

export interface OverviewNetworkPanelProps {
  device: DeviceDetail;
  trafficMinuteRx: string;
  trafficMinuteTx: string;
  trafficSpeedRx: string;
  trafficSpeedTx: string;
}

export function OverviewNetworkPanel({ device, trafficMinuteRx, trafficMinuteTx, trafficSpeedRx, trafficSpeedTx }: OverviewNetworkPanelProps) {
  const { t, lang } = useI18n();
  const developerActive = !!device.developerEnabled;
  const [publicIP, setPublicIP] = useState<PublicIPInfo | null>(null);
  const [detectingIP, setDetectingIP] = useState(false);
  const [liveSession, setLiveSession] = useState<{ phase?: string; modemPhase?: string; lastError?: string } | null>(null);
  const [consumeKB, setConsumeKB] = useState("10");
  const [consuming, setConsuming] = useState(false);
  const [consumeResult, setConsumeResult] = useState<DataConsumeResult | null>(null);
  const traffic = device.traffic || {};
  const metaStatus = device.trafficMeta?.status;
  const sampleNote = metaStatus === "waiting_sample" ? t("等待采样") : metaStatus === "stale" ? t("采样中断") : "";
  const off = !device.networkEnabled;
  const sessionPhase = liveSession?.phase || device.networkPhase || "unknown";
  const modemPhase = liveSession?.modemPhase || device.modemPhase || "";
  const sessionError = liveSession?.lastError || device.networkError || "";
  const sessionState = ({
    starting: t("正在开启"), stopping: t("正在关闭"), recovering: t("正在恢复"),
    connected: t("已连接"), disabled: t("已关闭"), failed: t("异常"), unknown: t("状态未知"),
  } as Record<string, string>)[sessionPhase] || t("状态未知");
  const displayedSessionState = modemPhase === "rebooting" ? t("模组重启中") : sessionState;
  const consumeAmount = Number(consumeKB);
  const consumeAmountValid = Number.isInteger(consumeAmount) && consumeAmount >= MIN_CONSUME_KB && consumeAmount <= MAX_CONSUME_KB;
  const canConsume = sessionPhase === "connected" && modemPhase !== "rebooting" && consumeAmountValid;

  const minuteRx = trafficMinuteRx || sampleNote || traffic.rx;
  const minuteTx = trafficMinuteTx || sampleNote || traffic.tx;
  const speedRx = trafficSpeedRx || sampleNote || traffic.rate || "--";
  const speedTx = trafficSpeedTx || sampleNote || traffic.rateTx || "--";

  useEffect(() => {
    let cancelled = false;
    setPublicIP(device.publicIpInfo?.detected ? device.publicIpInfo : null);
    if (!developerActive) return () => { cancelled = true; };
    api<PublicIPInfo>(`/devices/${encodeURIComponent(device.id)}/network/public-ip`)
      .then((info) => {
        if (!cancelled) setPublicIP(info.detected ? info : null);
      })
      .catch(() => {
        if (!cancelled) setPublicIP(null);
      });
    return () => { cancelled = true; };
  }, [
    developerActive,
    device.id,
    device.interface,
    device.networkEnabled,
    device.networkPhase,
    device.modem?.iccid,
    device.publicIpInfo?.detected,
    device.publicIpInfo?.ip,
    device.publicIpInfo?.countryCode,
    device.publicIpInfo?.region,
    device.publicIpInfo?.city,
    device.publicIpInfo?.organization,
  ]);

  useEffect(() => {
    let cancelled = false;
    setLiveSession(null);
    if (!developerActive) return () => { cancelled = true; };
    api<{ phase?: string; modemPhase?: string; lastError?: string }>(`/devices/${encodeURIComponent(device.id)}/network`)
      .then((status) => {
        if (!cancelled) setLiveSession(status);
      })
      .catch(() => {
        if (!cancelled) setLiveSession(null);
      });
    return () => { cancelled = true; };
  }, [
    developerActive,
    device.id,
    device.interface,
    device.networkEnabled,
    device.networkPhase,
    device.networkError,
    device.modemPhase,
    device.modem?.iccid,
  ]);

  async function detectPublicIP() {
    setDetectingIP(true);
    try {
      const info = await api<PublicIPInfo>(`/devices/${encodeURIComponent(device.id)}/network/public-ip`, { method: "POST", body: {} });
      setPublicIP(info);
    } catch (error) {
      message.error(apiMessage(error) || t("公网 IP 检测失败"));
    } finally {
      setDetectingIP(false);
    }
  }

  async function consumeData() {
    setConsuming(true);
    try {
      const result = await api<DataConsumeResult>(`/devices/${encodeURIComponent(device.id)}/network/consume`, { method: "POST", body: { kilobytes: consumeAmount } });
      setConsumeResult(result);
      if (result.complete) message.success(t("流量消耗完成"));
      else message.warning(t("下载提前结束，只消耗了部分流量"));
    } catch (error) {
      message.error(apiMessage(error) || t("消耗流量失败"));
    } finally {
      setConsuming(false);
    }
  }

  const consumeSummary = consumeResult
    ? [
        formatBytes(consumeResult.payloadBytes),
        consumeResult.interfaceMeasured ? `${t("网卡")} ${formatBytes(consumeResult.interfaceRxBytes + consumeResult.interfaceTxBytes)}` : "",
      ].filter(Boolean).join(" · ")
    : "";

  let countryName = publicIP?.countryCode || "";
  if (publicIP?.countryCode) {
    try {
      countryName = new Intl.DisplayNames([lang === "zh" ? "zh-CN" : "en"], { type: "region" }).of(publicIP.countryCode) || publicIP.countryCode;
    } catch {
      countryName = publicIP.countryCode;
    }
  }
  const location = publicIP
    ? [countryName, publicIP.region, publicIP.city].filter((value, index, values) => value && values.indexOf(value) === index).join(" · ")
    : "";

  if (!developerActive) {
    return null;
  }

  return (
    <div className="ui-panel-muted p-4">
      <div className="mb-2 text-xs font-bold uppercase tracking-wider text-gray-500">{t("网络")}</div>
      <div className="space-y-1.5 text-sm text-gray-700 dark:text-gray-200">
        <FieldRow label={t("数据会话")} value={displayedSessionState} valueTitle={sessionError || displayedSessionState} />
        <div className="flex w-full min-w-0 items-center justify-between gap-3">
          <span className="shrink-0 whitespace-nowrap text-gray-500">{t("公网 IP")}</span>
          <div className="flex min-w-0 items-center justify-end gap-2">
            <span className="truncate font-mono" title={publicIP?.ip || ""}>{publicIP?.ip || "-"}</span>
            <Button size="small" loading={detectingIP} disabled={off} onClick={() => void detectPublicIP()}>{t("检测")}</Button>
          </div>
        </div>
        <FieldRow
          label={t("国家/地区")}
          value={publicIP ? location : "-"}
          prefix={publicIP ? <CountryFlag countryCode={publicIP.countryCode} /> : null}
        />
        {off ? (
          <div className="flex items-center justify-center p-6 text-sm text-gray-400">{t("数据未开启")}</div>
        ) : (
          <>
          <FieldRow label={t("内网 IPv4")} value={device.privateIp} monospace copyable />
          <FieldRow label={t("近1分钟上传")} value={minuteTx} monospace />
          <FieldRow label={t("近1分钟下载")} value={minuteRx} monospace />
          <FieldRow label={t("实时下载速率")} value={speedRx} monospace />
          <FieldRow label={t("实时上传速率")} value={speedTx} monospace />
          <div className="flex w-full min-w-0 items-center justify-between gap-3">
            <span className="shrink-0 whitespace-nowrap text-gray-500">{t("消耗流量")}</span>
            <div className="flex min-w-0 items-center justify-end gap-2">
              <Input
                className="w-28"
                type="number"
                inputMode="numeric"
                min={MIN_CONSUME_KB}
                max={MAX_CONSUME_KB}
                step={1}
                value={consumeKB}
                suffix="KB"
                disabled={consuming}
                aria-label={t("消耗流量")}
                onChange={(event) => setConsumeKB(event.target.value)}
              />
              <Button size="small" loading={consuming} disabled={!canConsume} onClick={() => void consumeData()}>{t("消耗")}</Button>
            </div>
          </div>
          {consumeResult ? <FieldRow label={t("上次消耗")} value={consumeSummary} monospace /> : null}
          </>
        )}
      </div>
    </div>
  );
}

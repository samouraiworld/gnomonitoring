"use client";
import { useEffect, useState } from "react";

// Type à adapter à ta réponse
type Incident = {
    Moniker: string;
    Addr: string;
    Level: string;
    StartHeight: number;
    EndHeight: number;
    Msg: string;
    SentAt: string;
};
type ParticipationRate = {
    Addr: string;
    Moniker: string;
    ParticipationRate: number;
};
// ⚡ Cache mémoire (global au module)
const memoryCache: {
    blockHeight?: { data: number; timestamp: number };
    incidents?: { data: Incident[]; timestamp: number };
} = {};

const CACHE_DURATION_MS = 10_000; // 10s de validité

export function Dash(activeTab: "all" | "monthly" | "weekly") {
    // const [activeTab, setActiveTab] = useState<"all" | "monthly" | "weekly">("all");
    const [participationData, setParticipationData] = useState<{
        all?: ParticipationRate[];
        monthly?: ParticipationRate[];
        weekly?: ParticipationRate[];
    }>({});

    const [blockHeight, setBlockHeight] = useState<number | null>(null);
    const [incidents, setIncidents] = useState<Incident[]>([]);
    const [loading, setLoading] = useState<boolean>(true);
    const [error, setError] = useState<string | null>(null);
    const formatSentAt = (dateString: string) => {
        const date = new Date(dateString);
        return new Intl.DateTimeFormat("fr-FR", {
            day: "2-digit",
            month: "2-digit",
            hour: "2-digit",
            minute: "2-digit",
            hour12: false
        }).format(date);
    };

    const now = Date.now();
    const fetchParticipation = async (tab: "all" | "monthly" | "weekly") => {
        const periodMap = {
            all: "current_year",
            monthly: "current_month",
            weekly: "current_week",
        } as const;

        try {
            setLoading(true);
            const res = await fetch(`/api/rate?period=${periodMap[tab]}`);
            if (!res.ok) throw new Error("Failed to fetch participation");
            const data = await res.json();
            setParticipationData(prev => ({ ...prev, [tab]: data.rate }));
        } catch (err) {
            console.error("Error fetching participation:", err);
        } finally {
            setLoading(false);
        }
    };


    const fetchBlockHeight = async () => {
        const cached = memoryCache.blockHeight;
        if (cached && now - cached.timestamp < CACHE_DURATION_MS) {
            setBlockHeight(cached.data);
            return;
        }

        try {
            const res = await fetch("/api/block_height");
            if (!res.ok) throw new Error("❌ Failed to fetch block height");
            const data = await res.json();
            setBlockHeight(data.last_stored);
            memoryCache.blockHeight = { data: data.last_stored, timestamp: now };
        } catch (err: any) {
            setError(err.message || "Unknown error (block height)");
        }
    };

    const fetchIncidents = async () => {
        const cached = memoryCache.incidents;
        if (cached && now - cached.timestamp < CACHE_DURATION_MS) {
            setIncidents(cached.data);
            return;
        }

        try {
            const res = await fetch("/api/last_incident");
            if (!res.ok) throw new Error("❌ Failed to fetch incidents");
            const data = await res.json();
            setIncidents(data);
            console.log("Received incidents:", data);
            memoryCache.incidents = { data, timestamp: now };
        } catch (err: any) {
            setError(err.message || "Unknown error (incidents)");
        }
    };

    const fetchAll = async () => {
        setLoading(true);
        await Promise.all([
            fetchBlockHeight(),
            fetchIncidents(),

        ]);
        setLoading(false);
    };

    useEffect(() => {
        fetchAll();
        const interval = setInterval(fetchAll, 10000); // update auto
        if (!participationData[activeTab]) {
            fetchParticipation(activeTab);
        }

        return () => clearInterval(interval);
    }, [activeTab]);

    return {
        blockHeight,
        incidents,
        loading,
        error,
        participationData,
        formatSentAt,
        reload: fetchAll,
    };
}

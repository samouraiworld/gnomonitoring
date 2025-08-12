import { NextResponse } from "next/server";

export async function GET() {
    const backendURL = process.env.BACKEND_URL;

    try {
        const res = await fetch(`${backendURL}/latest_incidents`, {
            // Force Next.js à revalider (stale-while-revalidate)
            next: { revalidate: 30 }, // 30s
        });

        if (!res.ok) {
            throw new Error("Failed to fetch from backend");
        }

        const data = await res.json();

        return NextResponse.json(data, {
            status: 200,
            headers: {
                "Cache-Control": "public, s-maxage=30, stale-while-revalidate=59",
            },
        });
    } catch (err) {
        console.error("API proxy error:", err);
        return NextResponse.json({ error: "Failed to fetch lastest_incidents" }, { status: 500 });
    }
}

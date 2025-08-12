// app/api/block_height/route.ts
import { NextResponse } from "next/server";
export async function GET() {
    const backendURL = process.env.BACKEND_URL;

    try {
        const res = await fetch(`${backendURL}/block_height`);
        if (!res.ok) {
            throw new Error("Failed to fetch from backend");
        }

        const data = await res.json();
        return NextResponse.json(data);
    } catch (err) {
        console.error("API proxy error:", err);
        return NextResponse.json({ error: "Failed to fetch block height" }, { status: 500 });
    }
}
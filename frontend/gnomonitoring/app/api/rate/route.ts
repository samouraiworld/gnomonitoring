// app/api/rate/route.ts
import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
    const period = req.nextUrl.searchParams.get("period");
    const validPeriods = ["current_week", "current_month", "current_year"];

    if (!period || !validPeriods.includes(period)) {
        return new NextResponse("Invalid or missing period", { status: 400 });
    }

    const backendURL = process.env.BACKEND_URL;
    if (!backendURL) {
        return new NextResponse("Backend URL not configured", { status: 500 });
    }

    try {
        const res = await fetch(`${backendURL}/Participation?period=${period}`);
        if (!res.ok) {
            return new NextResponse(`Backend error: ${res.statusText}`, { status: res.status });
        }

        const data = await res.json();
        return NextResponse.json({ rate: data });

    } catch (err: any) {
        return new NextResponse(err.message || "Error contacting backend", { status: 500 });
    }
}

// app/api/update-report-hour/route.ts
import { NextRequest, NextResponse } from "next/server";

export async function PUT(req: NextRequest) {
    const body = await req.json();
    const { hour, minute, user_id } = body;

    if (hour === undefined || minute === undefined || !user_id) {
        return new NextResponse("Paramètres manquants", { status: 400 });
    }

    try {
        const backendURL = process.env.BACKEND_URL;
        const res = await fetch(`${backendURL}/usersH`, {
            method: "PUT",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ hour, minute, user_id }),
        });

        const data = await res.text();
        if (!res.ok) {
            return new NextResponse(data, { status: res.status });
        }

        return NextResponse.json({ success: true });
    } catch (err: any) {
        return new NextResponse(err.message || "Erreur serveur", { status: 500 });
    }
}
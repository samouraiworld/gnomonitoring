// app/api/edit-webhook/route.ts

import { auth } from "@clerk/nextjs/server";
import { NextResponse } from "next/server";

export async function PUT(req: Request) {
    const { userId } = await auth();
    if (!userId) {
        return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
    }

    try {
        const body = await req.json();
        const { ID, Description, URL, Type, target } = body;

        if (!ID || !URL || !Type || !target) {
            return NextResponse.json({ error: "Missing parameters" }, { status: 400 });
        }

        const endpoint =
            target === "govdao"
                ? `${process.env.BACKEND_URL}/webhooks/govdao`
                : `${process.env.BACKEND_URL}/webhooks/validator`;

        const response = await fetch(endpoint, {
            method: "PUT",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                ID: ID,
                Description: Description,
                UserID: userId,
                URL: URL,
                Type: Type,
            }),
        });

        if (!response.ok) {
            const errorText = await response.text();
            console.error("Erreur backend Go:", errorText);
            return NextResponse.json({ error: "Failed to update webhook" }, { status: 500 });
        }

        return NextResponse.json({ status: "ok" });
    } catch (err) {
        console.error("Erreur API edit-webhook:", err);
        return NextResponse.json({ error: "Internal server error" }, { status: 500 });
    }
}

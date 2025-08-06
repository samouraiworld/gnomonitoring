// app/api/edit-contact-alert/route.ts

import { auth } from "@clerk/nextjs/server";
import { NextResponse } from "next/server";

export async function PUT(req: Request) {
    const { userId } = await auth();
    if (!userId) {
        return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
    }

    try {
        const body = await req.json();
        const { ID, Moniker, NameContact, Mention_Tag, Id_Webhook } = body;

        if (!ID || !Moniker || !NameContact || !Mention_Tag || !Id_Webhook) {
            return NextResponse.json({ error: "Missing parameters" }, { status: 400 });
        }

        const endpoint = `${process.env.BACKEND_URL}/alert-contacts`;


        const response = await fetch(endpoint, {
            method: "PUT",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                ID: ID,
                Moniker: Moniker,
                NameContact: NameContact,
                Mention_Tag: Mention_Tag,
                Id_Webhook: Id_Webhook,

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

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
        const { id, moniker, namecontact, mention_tag, id_webhook } = body;
        console.log("id:", id);
        console.log("user_id:", userId);
        console.log("moniker:", moniker);
        console.log("namecontact:", namecontact);
        console.log("mention_tag:", mention_tag);
        console.log("id_Webhook:", id_webhook);

        if (!id || !moniker || !namecontact || !mention_tag || !id_webhook) {
            return NextResponse.json({ error: "Missing parameters" }, { status: 400 });
        }

        const endpoint = `${process.env.BACKEND_URL}/alert-contacts`;


        const response = await fetch(endpoint, {
            method: "PUT",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify({
                id: id,
                user_id: userId,
                moniker: moniker,
                namecontact: namecontact,
                mention_tag: mention_tag,
                id_webhook: Number(id_webhook),

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

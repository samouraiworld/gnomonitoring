// app/api/get-webhooks/route.ts

import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
    const user_id = req.nextUrl.searchParams.get("user_id");
    if (!user_id) {
        return new NextResponse("Missing user_id", { status: 400 });
    }

    const backendURL = process.env.BACKEND_URL;

    try {

        const [govRes, valRes, contactsRes, hourRes] = await Promise.all([
            fetch(`${backendURL}/webhooks/govdao?user_id=${user_id}`),
            fetch(`${backendURL}/webhooks/validator?user_id=${user_id}`),
            fetch(`${backendURL}/alert-contacts?user_id=${user_id}`),
            fetch(`${backendURL}/usersH?user_id=${user_id}`),
        ]);
        const [govData, valData, contactsData, hourData] = await Promise.all([ //, contactsData, configData
            govRes.json(),
            valRes.json(),
            contactsRes.json(),
            hourRes.json(),
        ]);


        return NextResponse.json({
            govWebhooks: govData,
            valWebhooks: valData,
            contacts: contactsData,
            hour: hourData,
            // config: configData, // optionnel
        });
    } catch (err: any) {
        return new NextResponse(err.message || "Erreur serveur", { status: 500 });
    }
}




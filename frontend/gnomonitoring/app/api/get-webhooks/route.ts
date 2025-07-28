// app/api/get-webhooks/route.ts

import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
    const user_id = req.nextUrl.searchParams.get("user_id");
    if (!user_id) {
        return new NextResponse("Missing user_id", { status: 400 });
    }

    const backendURL = process.env.BACKEND_URL;

    try {
        // Appel pour GovDAO
        const govRes = await fetch(`${backendURL}/webhooks/govdao?user_id=${user_id}`);
        const valRes = await fetch(`${backendURL}/webhooks/validator?user_id=${user_id}`);
        // const contactsRes = await fetch(`${backendURL}/contacts?user_id=${user_id}`);
        // const configRes = await fetch(`${backendURL}/config?user_id=${user_id}`); // si tu as un point de config heure/rapport

        const [govData, valData] = await Promise.all([ //, contactsData, configData
            govRes.json(),
            valRes.json(),
            // contactsRes.json(),
            // configRes.json()
        ]);


        return NextResponse.json({
            govWebhooks: govData,
            valWebhooks: valData,
            // contacts: contactsData,
            // config: configData, // optionnel
        });
    } catch (err: any) {
        return new NextResponse(err.message || "Erreur serveur", { status: 500 });
    }
}

// export async function GET() {
//     const backend = process.env.BACKEND_URL
//     const [govdaoRes, validatorRes] = await Promise.all([
//         fetch(`${backend}/webhooks/govdao`),
//         fetch(`${backend}/webhooks/validator`)
//     ])

//     if (!govdaoRes.ok || !validatorRes.ok) {
//         return new Response("Error fetching webhooks", { status: 500 })
//     }

//     const govdao = await govdaoRes.json()
//     const validator = await validatorRes.json()

//     return Response.json({ govdao, validator })
// }



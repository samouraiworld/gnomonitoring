// app/api/delete-webhook/route.ts
export async function POST(req: Request) {
    const { id, user_id, type } = await req.json();
    const backend = process.env.BACKEND_URL;

    const endpoint =
        type === "validator"
            ? `${backend}/webhooks/validator?id=${id}&user_id=${user_id}`
            : `${backend}/webhooks/govdao?id=${id}&user_id=${user_id}`;

    const res = await fetch(endpoint, { method: "DELETE" });

    if (!res.ok) {
        const text = await res.text();
        return new Response(text, { status: res.status });
    }

    return new Response("OK");
}

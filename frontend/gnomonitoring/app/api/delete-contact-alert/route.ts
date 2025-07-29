// app/api/delete-contact-alert/route.ts
export async function POST(req: Request) {
    const { id } = await req.json();


    const endpoint = `${process.env.BACKEND_URL}/alert-contacts?id=${id}`;


    const res = await fetch(endpoint, { method: "DELETE" });

    if (!res.ok) {
        const text = await res.text();
        return new Response(text, { status: res.status });
    }

    return new Response("OK");
}

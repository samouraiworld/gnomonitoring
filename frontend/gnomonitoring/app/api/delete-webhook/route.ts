// app/api/delete-webhook/route.ts

export async function POST(req: Request) {
    const { id, type } = await req.json()
    const backend = process.env.BACKEND_URL

    const endpoint =
        type === 'validator'
            ? `${backend}/gnovalidator?id=${id}`
            : `${backend}/webhooksgovdao?id=${id}`

    const res = await fetch(endpoint, { method: 'DELETE' })

    if (!res.ok) {
        const text = await res.text()
        return new Response(text, { status: res.status })
    }

    return new Response('OK')
}

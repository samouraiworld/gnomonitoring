// app/api/get-webhooks/route.ts

export async function GET() {
    const backend = process.env.BACKEND_URL
    const [govdaoRes, validatorRes] = await Promise.all([
        fetch(`${backend}/webhooksgovdao`),
        fetch(`${backend}/gnovalidator`)
    ])

    if (!govdaoRes.ok || !validatorRes.ok) {
        return new Response("Error fetching webhooks", { status: 500 })
    }

    const govdao = await govdaoRes.json()
    const validator = await validatorRes.json()

    return Response.json({ govdao, validator })
}

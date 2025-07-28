import { auth } from "@clerk/nextjs/server";
import { NextResponse } from "next/server";

export async function POST() {
    const userId = (await auth()).userId;
    console.log("userId:", userId);
    // const { userId } = auth();

    if (!userId) {
        return NextResponse.json({ error: "Unauthorized" }, { status: 401 });
    }

    try {
        const user = await fetch(`https://api.clerk.dev/v1/users/${userId}`, {
            headers: {
                Authorization: `Bearer ${process.env.CLERK_SECRET_KEY}`,
            },
        }).then(res => res.json());

        const body = {
            user_id: user.id,
            email: user.email_addresses[0]?.email_address || "",
            name: user.first_name + " " + user.last_name,
        };

        // Appel à ton backend Go (interne côté serveur donc invisible client)
        const response = await fetch(`${process.env.BACKEND_URL}/users`, {
            method: "POST",
            headers: {
                "Content-Type": "application/json",
            },
            body: JSON.stringify(body),
        });
        if (response.status === 409) {
            console.log("User already exists, skipping insert");
            return NextResponse.json({ status: "already_exists" });
        }

        if (!response.ok) {
            throw new Error("Failed to create user in backend");
        }

        return NextResponse.json({ status: "ok" });
    } catch (err) {

    }
}

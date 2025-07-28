"use client";
import { useEffect } from "react";
import { useUser } from "@clerk/nextjs";

export default function ConfigBotLayout({ children }: { children: React.ReactNode }) {
    const { user } = useUser();

    useEffect(() => {
        if (user) {
            fetch("/api/create-user", { method: "POST" });
        }
    }, [user]);

    return <>{children}</>;
}

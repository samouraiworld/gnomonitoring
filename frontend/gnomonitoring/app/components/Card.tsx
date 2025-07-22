import Link from "next/link";

export function Card({ title, link }: { title: string; link: string }) {
    return (
        <Link href={link}>
            <div className="rounded-2xl shadow-md p-6 bg-white hover:shadow-lg transition cursor-pointer">
                <h3 className="text-xl font-semibold text-gray-800">{title}</h3>
            </div>
        </Link>
    );
}
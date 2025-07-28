import {
    SignInButton,
    SignUpButton,
    SignedIn,
    SignedOut,
    UserButton,
} from '@clerk/nextjs'
export function Navbar() {
    return (
        <header className="bg-white shadow">
            <nav className="container mx-auto px-4 py-4 flex justify-between items-center">
                <a href="/" className="text-xl font-bold text-blue-600">GnoMonitoring</a>
                <div className="space-x-4">

                    <SignedOut>
                        <SignInButton />
                        <SignUpButton>
                            <button className="bg-[#6c47ff] text-white rounded-full font-medium text-sm sm:text-base h-10 sm:h-12 px-4 sm:px-5 cursor-pointer">
                                Sign Up
                            </button>
                        </SignUpButton>
                    </SignedOut>
                    <SignedIn>
                        <div className="flex items-center space-x-4">
                            <a
                                href="/configbot"
                                className="bg-gray-100 text-gray-700 hover:bg-gray-200 rounded-full font-medium text-sm sm:text-base h-10 sm:h-12 px-4 sm:px-5 flex items-center justify-center transition"
                            >
                                ⚙️ ConfigBot
                            </a>
                            <div className="relative">
                                <UserButton

                                    appearance={{
                                        elements: {
                                            userButtonAvatarBox: "w-10 h-10", // taille
                                        },
                                    }}
                                />
                            </div>
                        </div>

                    </SignedIn>
                </div>
            </nav>
        </header>
    );
}

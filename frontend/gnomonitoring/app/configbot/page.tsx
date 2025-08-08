"use client";
import { ConfigBot } from "@/app/configbot/configbot";

export default function ConfigBotPage() {
    const {
        user,
        isLoaded,
        dailyHour,
        dailyMinute,
        setDailyHour,
        setDailyMinute,
        govWebhooks,
        valWebhooks,
        contacts,
        handleWebhookChange,
        handleUpdateNewWebhook,
        setGovWebhooks,
        setValWebhooks,
        setContacts,
        loadConfig,
        handleSaveNewWebhook,
        handleDeleteWebhook,
        handleCreateContact,
        handleDeleteContact,
        handleUpdateContact,
        handleAddContact,
        handleContactChange,
        handleSaveHour,
        handleAddWebhook,
        sections,
    } = ConfigBot();

    if (!isLoaded || !user) return <p>Chargement...</p>;


    return (


        <div className="max-w-7xl mx-auto px-4 py-8 space-y-10">

            <h1 className="text-3xl font-bold text-center mb-8">Configure GnoMonitoring Alerts</h1>

            {/* USER */}
            <section className="bg-white dark:bg-neutral-900 rounded-2xl shadow-md p-6 space-y-4">
                <h2 className="text-xl font-semibold">User: {user?.primaryEmailAddress?.emailAddress}</h2>

            </section>
            {/* HOUR */}
            <section className="bg-white dark:bg-neutral-900 rounded-2xl shadow-md p-6 space-y-4">
                <h2 className="text-xl font-semibold">Set Daily Validator Report Time: </h2>

                <div className="flex items-center gap-3">

                    <input
                        type="time"
                        value={`${dailyHour.toString().padStart(2, '0')}:${dailyMinute.toString().padStart(2, '0')}`}
                        onChange={(e) => {
                            const [hour, minute] = e.target.value.split(":").map(Number);
                            setDailyHour(hour);
                            setDailyMinute(minute);
                        }}
                        className="border rounded px-3 py-1 text-center dark:bg-neutral-800 dark:border-neutral-600"
                    />
                    <button
                        onClick={handleSaveHour}
                        className="ml-4 bg-blue-600 hover:bg-blue-700 text-white px-4 py-2 rounded"
                    >
                        Save
                    </button>
                </div>
            </section>


            {sections.map(({ title, type, webhooks }) => (
                <section key={type} className="bg-white dark:bg-neutral-900 rounded-2xl shadow-md p-6 space-y-4">
                    <h2 className="text-xl font-semibold">{title}</h2>


                    {webhooks.map((w, i) => (
                        <div key={i} className="flex flex-col md:flex-row gap-2 md:items-center">

                            <input type="text" placeholder="Description" value={w.Description ?? ""}
                                onChange={(e) => handleWebhookChange(type, i, "Description", e.target.value)}
                                className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600" />


                            <input type="text" placeholder="URL" value={w.URL ?? ""}
                                onChange={(e) => handleWebhookChange(type, i, "URL", e.target.value)}
                                className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600" />


                            <select value={w.Type ?? ""}
                                onChange={(e) => handleWebhookChange(type, i, "Type", e.target.value)}
                                className="border rounded p-1 dark:bg-neutral-800 dark:border-neutral-600">
                                <option value="discord">Discord</option>
                                <option value="slack">Slack</option>
                            </select>


                            <button onClick={() => handleSaveNewWebhook(type, i)}
                                className="bg-green-600 text-white px-2 py-1 rounded text-sm hover:bg-green-700">
                                Save
                            </button>
                            <button onClick={() => handleUpdateNewWebhook(type, i)}
                                className="bg-green-600 text-white px-2 py-1 rounded text-sm hover:bg-green-700">
                                Update
                            </button>
                            <button onClick={() => handleDeleteWebhook(type, w.ID)}
                                disabled={!w.ID}
                                className={`${!w.ID ? "bg-gray-400 cursor-not-allowed" : "bg-red-600 hover:bg-red-700"} text-white px-2 py-1 rounded text-sm`}>
                                Delete
                            </button>

                        </div>
                    ))}



                    <button onClick={() => handleAddWebhook(type)}
                        className="text-blue-600 dark:text-blue-400 underline mt-2 hover:text-blue-800">
                        + Add Webhook {title.includes("Validator") ? "Validator" : "GovDAO"}
                    </button>
                </section>
            ))}

            {/* Contacts */}
            <section className="bg-white dark:bg-neutral-900 rounded-2xl shadow-md p-6 space-y-4">
                <h2 className="text-xl font-semibold">Contacts d'alerte</h2>
                {contacts.map((c, i) => (
                    <div key={i} className="flex flex-col md:flex-row gap-2 md:items-center">
                        <input type="text" placeholder="Moniker" value={c.MONIKER}
                            onChange={(e) => handleContactChange(i, "MONIKER", e.target.value)}
                            className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600" />
                        <input type="text" placeholder="Nom" value={c.NAME}
                            onChange={(e) => handleContactChange(i, "NAME", e.target.value)}
                            className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600" />
                        <input type="text" placeholder="Mention tag" value={c.MENTIONTAG}
                            onChange={(e) => handleContactChange(i, "MENTIONTAG", e.target.value)}
                            className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600" />


                        <select
                            value={c.IDWEBHOOK || ""}
                            onChange={(e) => handleContactChange(i, "IDWEBHOOK", e.target.value)}
                            className="border p-2 rounded w-full md:w-1/4 dark:bg-neutral-800 dark:border-neutral-600"
                        >
                            <option value="">Associate with a webhook...</option>
                            {valWebhooks
                                .filter((wh) => wh.ID !== undefined)
                                .map((wh) => (
                                    <option key={wh.ID} value={String(wh.ID)}>
                                        {wh.Description || wh.URL}
                                    </option>
                                ))}
                        </select>
                        <div className="flex gap-1">
                            <button onClick={() => handleCreateContact(i)}
                                className="bg-green-600 hover:bg-green-700 text-white px-2 rounded">
                                Save
                            </button>
                            <button onClick={() => handleUpdateContact(i)}
                                className="bg-green-600 hover:bg-green-700 text-white px-2 rounded">
                                Update
                            </button>
                            <button onClick={() => handleDeleteContact(c.ID)}
                                className="bg-red-600 hover:bg-red-700 text-white px-2 rounded">
                                Delete
                            </button>
                        </div>
                    </div>
                ))}
                <button onClick={handleAddContact} className="text-blue-600 underline mt-4 hover:text-blue-800">
                    + Add a contact
                </button>
            </section>

        </div>

    );
}
